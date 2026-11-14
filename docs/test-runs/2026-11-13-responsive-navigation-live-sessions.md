# Responsive navigation and live-session UI walkthrough — 2026-11-13 (task 4733)

- **Date:** 2026-11-13, 02:55–03:55 UTC, one run.
- **Task:** task 4733, branch
  `grain/098073e4/task-4733-walk-through-responsive`, base `main`.
- **Commit tested:** `4ae6438fd` ("Merge pull request #1435"), build stamp
  `v0.0.0-20261113022244-4ae6438fdcd4 · 4ae6438 · 2026-11-13 02:22Z`. The
  two fixes below were rebuilt on top of that commit and checked again on
  a second demo with a fresh store.
- **Earlier runs in this series:** most recently
  `2026-11-11-responsive-navigation-live-sessions.md` (task 4305). This run
  used new, independent drivers. It also covered most of what 11-11 listed
  as not covered:
  - Pause and Resume on a task-bound interactive chat;
  - the inline `request_access`, local-script and config-change cards;
  - Adopt shipped defaults;
  - the stale-tab conflict retry;
  - the next-run notice and Apply now;
  - the phone stale-tasks row;
  - verified identity in the rail and the drawer.

  Following the "two UIs" rule, it also swept the Angular shell at
  `/next/`.
- **Result:**
  - The classic shell and sessions passed everything listed below, in
    Chromium and WebKit.
  - The `/next/` sweep found two layout faults with one cause, and both
    were fixed: Bug 1, `/next/` dialogs ran off both edges of a phone;
    Bug 2, the phone More sheet rose past the top of the screen.

## Environment

- Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0; Playwright
  1.62.1 from `ui/package-lock.json`. Headless Chromium and WebKit
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC.
- Built with `cd ui && npm ci && npm run build`,
  `cd ui-ng && npm ci && npm run build`, then
  `go build -o /var/tmp/grain ./cmd/grain`.
- Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file
<file> -observability -staging -config-change -host-mode-install
-merge-rules -mechanical-port`, with `TMPDIR` pointed at a fresh scratch
  directory, so the store was a throwaway embedded SQLite file. The fixed
  build ran the same way on `127.0.0.1:8421` over its own store, for the
  before/after measurements.
- There was no `/home/debian/.gemini-api-key`, so no model was called. The
  demo's sessions answer with its built-in fake agent. With no key,
  `LOOK=1` could not describe screenshots either, and this sandbox's file
  reader shows a PNG as bytes. Layout was therefore asserted through live
  DOM geometry:
  - `getBoundingClientRect`;
  - `elementFromPoint` hit tests;
  - `scrollWidth` against `innerWidth`;
  - scroll containers and `window.scrollY`.

  Contrast was checked with `@axe-core/playwright`'s `color-contrast` rule.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout, in `/var/tmp/walk`. Each browser context signs in through
  `POST /api/auth/access-code` (name `alice@example.com`) and then acts
  only through visible controls: rail and drawer items, tabs, header
  buttons, menus, the composer, dialogs. Every page recorded:
  - uncaught page errors and console errors;
  - failed requests and `/api/` responses of 400 or more;
  - every native dialog, with its text and whether it was accepted.

  The REST API (bearer access code) was used only for these:
  - reading back what a gesture had persisted;
  - filing one interactive task (`POST /api/tasks {"interactive": true}`,
    task 27) and one long-titled task;
  - creating the standing sessions for the live-controls pass;
  - making the "other tab's" edit in the stale-tab case.

### Viewports

| Name          | Size                | Context                       |
| ------------- | ------------------- | ----------------------------- |
| Desktop       | 1440×900            | fine pointer, keyboard        |
| Laptop        | 1024×768            | fine pointer, keyboard        |
| Narrow tablet | 720×900             | header overflow case          |
| Tablet        | 820×1180            | `hasTouch`                    |
| Phone         | 390×844             | `hasTouch`, `isMobile`        |
| Narrow phone  | 320×568             | `hasTouch`, `isMobile`        |
| Rotated       | 844×390, 568×320    | resized from portrait         |
| Live resize   | 1440→390→1440       | on each board, in one page    |

## Stubs

The server, store, built frontends, browsers and gestures were real
throughout. Only these boundaries were intercepted, each with a Playwright
`page.route` on that one page:

1. **A hung daemon.** Every `/api/**` request was held unanswered, then
   aborted when the route was removed, so the server answered again with
   no reload. This is a daemon that accepts connections but never
   answers.
2. **A viewer role.** `GET /api/config`'s `access` was rewritten to
   `grain/viewer` with read-only scopes. This tests only what the UI
   offers; `pkg/ui`'s route-scope tests cover the server's refusals.
3. **A failed sessions read.** `GET /api/console-sessions` answered
   `500 {"error":"store unavailable"}` on a cold load of `/sessions`.
4. **A slow session start.** `POST /api/console-sessions` was held behind
   a latch, so the optimistic first message, a queued follow-up and an
   unsent draft could be observed before the session existed.
5. **A failing task list.** `/api/tasks` (the live poll, not
   `state=closed`) answered `500 {"error":"synthetic tasks 500"}` until
   the route was removed.
6. **Missing shipped defaults.** `GET /api/console-sessions` gained
   `missingShippedDefaults: ["…api#ci-read"]` on the session under test.
   The `PUT` that Adopt shipped defaults sends went to the real server.
7. **A run in flight, with approval cards.** `GET /api/tasks` and
   `/api/tasks/{id}` showed the session's task as `running`, with an open
   attempt and three parked cards:
   - `pendingAccess` (read `acme/docs`);
   - `pendingConfigChange`, copied from the demo's real task 25;
   - one pending `localScriptApprovals` entry (`deploy_preview`).

   `POST …/cancel` (Apply now) was answered `{}`, because no real run was
   open to stop.
8. **Verified identity.** `GET /api/config` was given
   `authMethod: "iap"` and a 64-character `actor`, and `GET /api/whoami`
   that identity, as `ui/e2e/phone-signed-in-logout.spec.js` does.
   `POST /api/auth/logout` and the identity-aware proxy's clear-cookie
   page were fulfilled locally.

Nothing else was stubbed.

## Steps and outcomes

### 1. Navigation sweep (Chromium and WebKit)

About 280 checked steps per engine across 1440, 1024, 820, 390 and 320
widths:

- All navigation went through the real rail at 1440, 1024 and 820, and
  through the drawer (Open navigation) at 390 and 320.
- Settings went through `settings-nav-*` (desktop and phone) and the
  `settings-section-select` picker (820).
- System went through the tab strip, or the phone's System list with All
  of System to go back.

After every step the driver asserted:

- no horizontal page overflow;
- no visible interactive control past either side edge, unless it sat
  inside a scroll container;
- no `Admin mode`, `All teams` or team switcher text;
- on desktop and tablet, one 77 px header whose Alerts, System and
  Settings buttons win their own hit test;
- on a phone, the bar's Open navigation, Settings and New task on
  screen, and the drawer closed after each navigation.

| Area                                                               | Outcome                                                                                                                                                                                                                         | Verdict |
| ------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Inbox, Tasks, Running, Blocked, Boards, Sessions, Processes, Repos | Crumbs read `inbox`, `tasks` (Running and Blocked are task views), `boards`, `sessions`, `processes`, `repositories`.                                                                                                            | PASS    |
| `acme/widgets` tabs                                                | Overview, CI, Merge queue, Agents, Branches, Releases and Advanced. Crumbs read `repositories / acme/widgets / <tab>`.                                                                                                          | PASS    |
| Settings, 20 sections                                              | Crumbs read `settings / <section>`, including `settings / hosts` and `settings / config repo`. On a phone, All settings goes back each time.                                                                                     | PASS    |
| System, 8 tabs                                                     | Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics and Power, with crumbs `system / <tab>`. Back from Power gives Metrics, Forward gives Power, and a hard reload keeps `/system/power`.                                    | PASS    |
| Task detail and New task                                           | The first row opens `task 24`. The rail's + New task, or the phone bar's New task, opens `new task`.                                                                                                                             | PASS    |
| Deep links                                                         | `/ci` and `/director`. `/t/acme/repos`, `/t/core/tasks/1` and `/t/x/system/metrics` open `/repos`, `/tasks/1` and `/system/metrics`, team-less.                                                                                 | PASS    |
| Console                                                            | No page errors. The only failed request is `GET /api/repos/acme/widgets/github-branches` 404 on task panes: the demo has no GitHub branch lister, as earlier reports record.                                                     | PASS    |

### 2. Boards and visualizations

| Step                                       | Outcome                                                                                                                         | Verdict |
| ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Board, Timeline, Report, Tree at 1440      | Crumbs read `boards / Board`, `boards / Timeline`, `boards / Report` and `boards / Tree`, with no overflow.                     | PASS    |
| Live resize 1440 → 390 → 1440 on each      | At 390 the rail leaves, Open navigation appears, and there is no overflow. Back at 1440 the rail returns.                       | PASS    |
| Each board at 390×844 and 320×568          | No page overflow (the Timeline scrolls inside its own container).                                                               | PASS    |

### 3. Header rail and overflow (720×900)

| Step          | Outcome                                                                                                                                                                                       | Verdict |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Fit           | On `/`, `/repos`, `/sessions` and `/processes`, More page actions shows and the header stays 77 px, one row. `/boards` and `/inbox` fit without it. Every page's count is level with its title (Δy 0). | PASS    |
| Open / Escape | Opening sets `aria-expanded="true"`. Escape closes the slot and hands focus back to `page-header-more`.                                                                                       | PASS    |
| Outside press | Closes the slot.                                                                                                                                                                              | PASS    |
| Tab           | Tabbing from the build stamp onto the wrapped Filters (1) opens the slot by itself.                                                                                                           | PASS    |

### 4. Rail

| Step             | Outcome                                                                                                                                                                       | Verdict |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Collapse sidebar | 232 → 60 px, and still 60 after a reload. `[` with nothing focused expands it again. Typing `[[` into Search tasks… does not toggle it.                                         | PASS    |
| User tile        | `alice@example.com (access code)`. It opens Settings → Access. Collapsed, it is an icon whose native `title` carries the same label.                                          | PASS    |
| Customize        | Show CI, then Save, adds CI to a second open tab's rail without a reload (0 → 1). The setting was then restored.                                                              | PASS    |
| Staging          | `docs-preview` (`/staging/docs-preview/`, running) and `grain-staging` (`http://127.0.0.1:4280/`, crash-looping) are listed, each with a Pin button.                            | PASS    |
| Session lit      | A new session's rail entry is selected while it is open (`Mui-selected`).                                                                                                     | PASS    |

### 5. Phone bar and drawer (390×844, 320×568)

| Step         | Outcome                                                                                                                                                                                                                                                                      | Verdict |
| ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Drawer       | `aria-modal="true"`, with the System row. At 390 the user tile is at y 753–797 and the build stamp at 807–830 of 844; at 320, 477–521 and 531–554 of 568. The stamp is a `noWrap` ellipsis with its full text in its title.                                                    | PASS    |
| Stays open   | It stays open under the create menu (New session is listed) and after Escape from it. It also stays open when a staging Pin is tapped.                                                                                                                                        | PASS    |
| Closes       | Keyboard Enter on Inbox navigates and closes the drawer, and focus returns to Open navigation.                                                                                                                                                                               | PASS    |
| No bleed     | A wheel over the open drawer leaves `window.scrollY` at 0.                                                                                                                                                                                                                   | PASS    |
| Alerts panel | 16..374 of 390 and 16..304 of 320, in both engines. Escape returns focus to the bell.                                                                                                                                                                                       | PASS    |
| Long content | A task with a 160-character unbroken title, a wide markdown table and a 300-character code line has no page overflow at 390 or 320.                                                                                                                                          | PASS    |
| Rotation     | 844×390 and 568×320: no overflow, no clipped controls.                                                                                                                                                                                                                       | PASS    |
| Stale row    | With `/api/tasks` failing (stub 5), `phone-tasks-stale` appears under the controls at 390×32 px: "task list not refreshing", with retry. Once the server answers, Retry clears it.                                                                                         | PASS    |

### 6. Verified identity (stub 8)

| Step            | Outcome                                                                                                                                                                                                                                          | Verdict |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------- |
| Rail            | The tile reads `Acting as <64-char address>`, with a compact Log out (26×26) inside the rail and the build stamp below it. Collapsed, Log out is still inside the 60 px rail.                                                                      | PASS    |
| Phone drawer    | The `Signed in as` row holds the address and, under it, Log out (y 753–797 at 390, 477–521 at 320), with the stamp under that. The long address is ellipsized (391 of 170 px), and its full text is the row's `title` and accessible name. | PASS    |
| Opens Access    | The row opens `/settings/access` and closes the drawer.                                                                                                                                                                                          | PASS    |

At 1440×900 and 1024×768 the rail's tile and stamp are below the fold (y
956 and 993), reached by scrolling the rail. This is by design: beside
the page, the rail scrolls as one pane (`Sidebar.jsx` `pinFooter`, and
`ba2bc7a66`). Only the phone drawer pins its foot. The driver first
flagged this; it is noted here, not filed.

### 7. Banners

| Step                                | Outcome                                                                                                                                                                                                                                         | Verdict |
| ----------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Hung daemon (stub 1), 1440 and 390  | "This deployment cannot be reached: it accepted the connection but sent no answer within 10 seconds. Unreachable since … -- retrying" appears above the host-mode install notice. At 1440 they sit at 0–48 and 48–116; at 390, at 0–128 and 128–256. Both are full width, with no overflow. | PASS    |
| Recovery                            | When the daemon answers again, the banner clears without a reload, in both engines.                                                                                                                                                           | PASS    |

### 8. Sessions (Chromium and WebKit)

| Step                                     | Outcome                                                                                                                                                                                                                                                                                                                                         | Verdict |
| ---------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| New session from Create options          | Opens `/session/new` with `Message` focused, plus Session name, the model picker and Pin to sidebar.                                                                                                                                                                                                                                             | PASS    |
| Held start (stub 4)                      | The first message shows at once with `Sending…`. A second send queues as its own bubble. The unsent draft stays in the box.                                                                                                                                                                                                                     | PASS    |
| Session opens                            | It opens at `/session/walk-ch-…` (WebKit: `walk-we-…`) with `unsent draft text` in its composer. It is named, pinned and lit in the rail. Both messages are still there after a reload. `GET /api/console-sessions` lists it with its title.                                                                                                     | PASS    |
| Delete, declined then accepted           | Declining the confirm keeps the session, with focus on Delete session. Accepting removes it. Its sidebar pin goes, and stays gone after a reload.                                                                                                                                                                                                | PASS    |
| Deep link to the deleted session         | "There is no session “walk-ch-…” here. It may have been deleted; pick another below." above the list, not another session's chat.                                                                                                                                                                                                                | PASS    |
| `/sessions` failed read (stub 3)         | "The sessions could not be loaded (store unavailable)." It shows no empty state and no count.                                                                                                                                                                                                                                                    | PASS    |
| Pause / Resume on task-bound chat        | Task 27 (interactive, not a session) shows TaskChat with Pause beside Send, at 1440 and 390. Pause → `POST /api/tasks/27/pause` 200 → Resume, which survives a reload (`pause.by` = `alice@example.com (access code)`). Resume → `POST …/resume` 200 → Pause. No overflow.                                                                         | PASS    |
| Live controls                            | Edit `<title>` capabilities opens with "No run is open on this session…". It lists the API defaults including `api#ci-read`, says "every session reads every repository this deployment…", and has the model control.                                                                                                                         | PASS    |
| Network `none`                           | `PUT {"networkAccess":"none"}` saves `none`, while the server's `effectiveNetworkAccess` stays `open`. `session-network-not-isolated` warns of this. The choice survives a reload, and was then restored.                                                                                                                                       | PASS    |
| Escape in Add grant…                     | Escape closes only the menu. The capabilities region stays open, and focus returns to Add grant….                                                                                                                                                                                                                                                | PASS    |
| Stale-tab conflict                       | An "other tab" adds `api#tasks:read` through the API. Then Remove `api#capabilities:detach` in the pane sends a `PUT` that gets 409, re-reads, and sends a second `PUT` that gets 200. The server keeps both edits (18 + 1 − 1 = 18 grants, `tasks:read` present).                                                                                | PASS    |
| Adopt shipped defaults (stub 6)          | `adopt-shipped-defaults-banner` shows. Its button sends `PUT {"adoptShippedDefaults":true}`.                                                                                                                                                                                                                                                     | PASS    |
| Run in flight and cards (stub 7)         | The pane shows "A run is open on this session now…". The chat shows the request_access card ("Grant read to acme/docs…"), the config-change card ("Raise the default poll interval to 30s") and the local-script card (`deploy_preview`). Saving network access then offers Apply now, which posts `…/cancel`, and the notice goes. No overflow at 1440 or 390. | PASS    |
| Reduced motion                           | With `reducedMotion: "reduce"`, the typing dots have `animation-name: none`.                                                                                                                                                                                                                                                                     | PASS    |
| Start a fresh chat                       | Offers "Close this chat and start a fresh one" and "Start fresh with summary". The second sends `POST …/restart {"carryHandover":true}`. The session moves to a new task (31 → 32), and the old exchange is not shown.                                                                                                                         | PASS    |
| Contrast                                 | `color-contrast` found 0 violations on `/`, `/inbox`, `/sessions`, `/session/director`, `/tasks/27`, `/settings/access` and `/system/metrics`, in light and dark.                                                                                                                                                                                | PASS    |

### 9. Role-gated rendering (stub 2)

For a viewer:

- the rail's + New task is disabled;
- New session's Send is disabled;
- the task-bound chat's Pause is disabled.

### 10. `/next/` (Angular) shell

This pass covered 1440×900, 820×1180, 390×844 and 320×568, in light and
in dark at desktop and phone sizes:

- the desktop rail's primary destinations;
- the phone's compact bar, and every link in its More sheet;
- deep links to `/next/tasks/1`, `/next/sessions`, `/next/settings`,
  `/next/repos` and `/next/ci`.

Chromium ran this pass over the base build, and WebKit over the fixed
one. Each destination rendered with no page overflow and no control past
either side edge. The exceptions were the two faults below. In each one
the control was still reachable, but its box ran off the screen. The
desktop rail's More opens its "More navigation options" list
(`aria-expanded="true"`), which holds Customize navigation.

## Bug 1 — `/next/` dialogs ran off both sides of a phone (fixed)

**Reproduction:** open `/next/sessions` at 390×844 and press New session,
or measure any `.g-dialog`. The same applies to Contact running agents on
`/next/agents`, Run workflow on `/next/ci`, and New schedule on
`/next/processes`. Before the fix:

| Dialog                         | 390 (light and dark) | 1440      |
| ------------------------------ | -------------------- | --------- |
| New session, Contact, Run workflow | −9..399 of 390   | 570 wide  |
| Schedule (`.is-drawer`)        | −9..399 of 390       | 730 wide  |
| At 320                         | −9..329 of 320       |           |

**Cause:** `.g-dialog` is `width: min(520px, calc(100vw - 32px))` in
content-box, so its 24 px padding and 1 px border were added on top. That
made it 50 px wider than the space it was given.

**Fix:** `.g-dialog { box-sizing: border-box; }` in `ui-ng/src/styles.css`.
After the fix:

- at 390, every dialog above is at 16..374;
- at 1440, dialogs are 520 px wide (460..980), and the drawer is 680 px
  (380..1060), which are the widths their rules declare.

The design book (§4 Rules) now says a fixed box sized against `100vw` is
border-box.

## Bug 2 — the phone More sheet rose past the top of the screen (fixed)

**Reproduction:** open `/next/inbox` at 320×568 and press More in the
compact bar. The sheet's top is at y −8, and at 390×844 it is at −4. The
same happens at 844×390 and 390×664, in Chromium and WebKit. The first
row sits flush with the screen's top edge.

**Cause:** the same as Bug 1. `max-height: calc(100dvh - 72px)` was meant
to leave 16 px under the top of the screen, but the sheet's 8 px + 16 px
padding were added on top of it.

**Fix:** `.more-sheet { box-sizing: border-box; }`. The sheet's top is now
at y 16 at 320×568 and at 390×844. Its last entry, Customize navigation,
scrolls into reach inside it.

**Regression coverage for both bugs:** the new
`ui-ng/e2e/next-dialog-fits-phone.spec.js`, tagged `@phone`, so the phone
project runs it as well as desktop:

- New session's box and its Cancel and Start session buttons are inside
  320 and 390 viewports, with the 16 px margin;
- the dialog is 520 px wide and centred at 1440;
- the More sheet's top is on screen at 320×568, 390×844 and 844×390, and
  its last entry scrolls into reach.

The New session cases and the desktop-width case failed on the base build
(5 failed) and pass with the fix. The More sheet cases pass with the fix,
and the base's −8/−4 tops were measured on the base demo. In all, 11/11
pass. `next-full-system.spec.js` and `shell.spec.ts` pass with both fixes
(9/9).

Before and after measurements and screenshots were taken over the base
(`:8420`) and fixed (`:8421`) demos:

- the four dialogs, at 390×844 and 1440×900, in light and dark;
- the More sheet, at 320×568 and 390×844.

They are attached to the task's closing note, not committed.

## Not covered, and why

- **Run a suite dialog.** The driver did not find its opener on
  `/next/processes`. It shares `.g-dialog`, so the CSS fix and the
  injected-box measurement cover it, but it was not opened.
- **The classic session's composer with the capabilities panel open on a
  phone.** The pane and the composer scroll together inside
  `console-session-body` (5825 px of content in 602 px). So Send scrolls
  out of view while someone reads the grants, and comes back when they
  scroll down. It is reachable, and this matches the single-pane design,
  so it is noted here, not filed.
- **Live model streams.** With no Gemini key, the demo's fake agent
  answers sessions. Incremental streaming was not exercised beyond the
  typing indicator and the optimistic bubbles.

## Notes

- Driver corrections, not product faults:
  - The first stale-tab attempt toggled a non-capability checkbox ("file
    its tasks approved"). It was re-run on a grant removal (above).
  - The verified-identity driver first expected Log out below the
    signed-in row rather than inside it under the address, and a `title`
    on the email text rather than on the row.
  - The `/next/` driver first looked for the desktop More items under
    `role=menu`. They are a list, as described in §10.
  - The classic sweep's own board-link selector missed the board list,
    whose landmark is `role="main"` on a `div`. The boards were walked
    in §2 instead.
- On the fixed build, a bare `.g-dialog` and its `.is-drawer` form,
  injected into `/next/sessions`, both measure 16..304 at 320,
  16..374 at 390 and 460..980 at 1440. The base build gave −9..329,
  −9..399 and 435..1005. The classic UI's `.modal-dialog` was already
  inside the screen (16..304 at 320), and was not changed.
- `/next/`'s New session dialog and session list still say "Visible to
  team" and "Team: default". The classic shell has no teams. The Angular
  UI's session teams are its own, and they were left alone here.

---

# Second run — 2026-11-13, task 5391 (`/next/` first)

- **Date:** 2026-11-13, 14:11–15:00 UTC, one run.
- **Task:** task 5391, branch
  `grain/098073e4/task-5391-walk-through-responsive`, base `main`.
- **Commit tested:** `e69d34223` ("Merge pull request #1478"), build stamp
  `v0.0.0-20261113…-e69d34223…`. The fixes below were rebuilt on top of it
  (`fe0a1b857`, `79db6d021`) and walked again over a fresh store each time.
- **Why a second run:** the morning run (task 4733, above) walked the
  classic shell in depth and swept `/next/` for layout only. This run
  follows the "Two UIs" rule the other way round: every step ran on its
  `/next/` page first, and on the classic page only where `/next/` has
  none yet.
- **Result:** three faults found in `/next/`, all three fixed here with
  `ui-ng/e2e` coverage (Bugs 3–5). Two gaps where `/next/` lacks what
  the classic shell has were filed as follow-ups, not built here. The
  classic pages walked here passed.

## Environment

- Same sandbox image as the morning run: Debian, `linux/amd64`, Go
  1.26.10, Node.js v20.19.0, Playwright 1.62.1 (from
  `ui-ng/package-lock.json`). Headless Chromium and WebKit with
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, timezone UTC.
- Built with `npm ci && npm run build` in `ui/` and `ui-ng/`, then
  `go build -o /var/tmp/grain ./cmd/grain` (rebuilt after each fix).
- Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file
  <file> -observability -staging -config-change -host-mode-install
  -merge-rules -mechanical-port`, with `TMPDIR` set to a fresh scratch
  directory, so every start used a new throwaway embedded SQLite store.
- There was no `/home/debian/.gemini-api-key`, so no model was called.
  Sessions were answered by the demo's built-in fake agent, and `LOOK=1`
  could not describe screenshots. This sandbox's file reader shows a PNG
  as bytes, so layout was asserted from live DOM geometry instead:
  `getBoundingClientRect` on every visible control, `scrollWidth`
  against `innerWidth`, and `window.scrollY`. Contrast was checked with
  `@axe-core/playwright`.
- **How it was driven:** throwaway Node scripts in `/var/tmp/walk`,
  outside the checkout, using `playwright` from `ui-ng/node_modules`.
  - Each context signs in through `POST /api/auth/access-code` as
    `alice@example.com`, then acts only through visible controls.
  - Every page logged uncaught errors, console errors, failed requests,
    `/api/` responses of 400 or more, and every native dialog.
  - The REST API was used only to file one interactive task
    (`POST /api/tasks {"interactive": true}`, task 27) and to read back
    what a gesture had persisted.

### Viewports

| Name         | Size      | Context                         |
| ------------ | --------- | ------------------------------- |
| Desktop      | 1440×900  | fine pointer, keyboard          |
| Laptop       | 1024×768  | fine pointer                    |
| Tablet       | 820×1180  | `hasTouch`                      |
| Phone        | 390×844   | `hasTouch`, `isMobile`          |
| Short phone  | 390×664   | `hasTouch` (Send reachability)  |
| Small phone  | 320×568   | `hasTouch`                      |
| Live resize  | 1440→390→844×390→1440 | one page, Inbox     |

## Stubs

The server, store, built frontends, browsers and gestures were real
throughout. Only these were intercepted, each with a `page.route` on one
page:

1. **A failed sessions read.** `GET /api/console-sessions` answered `500`
   with body `boom` on a cold load of `/next/sessions`.
2. **A viewer role.** `GET /api/config`'s `access` was rewritten to
   `grain/viewer` with read-only scopes (`tasks:read`, `sessions:read`,
   `deployment:read`, and in the walkthrough every `GET` scope in
   `generated-routes.ts` ending `:read`). This tests only what the UI
   offers. `tests/security`'s route matrix covers what the server
   refuses.
3. **A hung, then refused, daemon.** Every `/api/**` request was held
   for 60 s for 25 s of wall time, then aborted with
   `connectionrefused` for 15 s, then let through again with no reload.

## What was walked, and on which UI

| Step | UI | Outcome |
| ---- | -- | ------- |
| Rail / compact bar to every destination (Inbox, Workstreams, Tasks, Agents, Sessions, Processes, CI, Telemetry, Context, Fleet), all five sizes | `/next/` | pass, Chromium and WebKit |
| All 20 Settings sections, by their links | `/next/` | pass |
| Fleet tabs (Sandboxes, Host Daemons, Pauses & Controls, Daemon Logs) | `/next/` (System → Fleet) | pass |
| Repo page `acme/widgets` and its tabs (Overview, CI & Workflows, Releases) | `/next/` | pass |
| Task list → task panel, Back, Forward, hard reload | `/next/` | pass (the list opens a task as `?task=<id>`) |
| New task sheet: open, Escape | `/next/` | pass |
| Old `/t/<team>/…` links | both | classic pass; `/next/` **Bug 3** |
| Deep links `/next/tasks/3`, `/next/director`, `/next/system`, `/next/metrics` | `/next/` | pass (redirect to `task/3`, `sessions/director`, `fleet`, `telemetry`) |
| Sessions: load error, New session (focus, Escape, focus return), first message, markdown table, back-to-back sends, reload, settings panel, Fresh chat, Delete (decline, then confirm), deep link to a deleted session | `/next/` | pass, except **Bug 4** |
| Interactive task chat: send, Pause, reload, Resume | `/next/` | pass, persisted |
| Viewer role across the shell, Sessions and a task | `/next/` | **Bug 5** |
| Contrast (axe), 8 pages × light/dark × desktop/phone | `/next/` | 0 serious or critical |
| Keyboard: Tab order, rail More and New menus, Escape, outside click | `/next/` | pass |
| Phone More sheet: on screen, Escape, outside tap, no background scroll | `/next/` | pass |
| Reduced motion: running animations on the Director while sending | `/next/` | none |
| Hung / refused daemon | `/next/` | follow-up (gap) |
| Boards and a board | classic only | pass |
| `[` collapse, persisted across reload, ignored while typing | classic only | pass |
| Phone drawer: modal, closes on navigation, build stamp on screen | classic only | pass |

**Steps with no `/next/` page yet:** Boards and their visualizations; the
classic rail's pinned items, Customize pinned items, Staging list and
`UserTile`; the `[` collapse shortcut; the phone drawer; the disconnected
banner, host-mode install notice and stale-tasks row (see follow-up A).
`/next/`'s "New board" item in the create menu leads to `/next/tasks?newBoard=1`.

### Details

- **Rail and compact bar.** At ≥820 px the rail's items are direct, with
  More (`aria-expanded`) holding Customize navigation. At 390 and 320 the
  compact bar holds Inbox, Tasks and Sessions, and the More sheet holds
  the rest. Every click landed on the destination's own `h1`. No page
  overflowed sideways and no visible control sat past either edge, at
  any size, in either engine. The More sheet measured 0..390 × 16..788
  at 390×844.
- **Live resize.** On one Inbox page, resizing 1440 → 390 swapped the
  rail for the compact bar. 844×390 and back to 1440 kept the page
  inside the screen.
- **Keyboard.** Tab from the top reaches the brand mark, New task, New,
  every rail item, More, Settings, then the header (Next up, live pill,
  Director, filter, theme, help, account), then the page. Escape on the
  rail's More and New menus closes them and returns focus to their
  button. An outside click closes More.
- **Sessions.**
  - With `/api/console-sessions` failing, `/next/sessions` shows "Sessions
    could not be read: …" rather than an empty list.
  - New session focuses Name. Escape closes it and returns focus to New
    session. Start session is on screen at 390×664 (617..657 of 664).
  - The first message appeared in the chat 190 ms after Start session.
  - A markdown table rendered as a table. Two sends in quick succession
    both showed at once, and both were there after a reload.
  - Settings shows Model ("Deployment default (gemini-2.5-pro)"), Network
    access, Keep running, Tasks approved, Reads all and the API grants.
    Escape closes it and returns focus to its toggle.
  - Start a fresh chat on the Director offers a handover and a clean
    start. A clean start emptied the chat, which stayed empty after a
    reload.
  - Delete: Escape declines and returns focus to Delete. Confirm deletes
    and goes to `/next/sessions`. A deep link to the deleted session
    shows the list on desktop, and "That session does not exist. Back to
    sessions" on a phone. Neither shows another session's chat.
- **Interactive task chat (task 27).** A message showed at once. Pause →
  `paused` set on the server. After a reload Resume was offered, and
  Resume cleared it. Same at 390.
- **Classic.** Boards opened from the rail (`/boards`, four boards) and a
  board (`/boards/board-1`) with no overflow. `[` collapsed the rail. That
  survived a reload, and a `[` typed into search went into the field.
  On a phone the drawer is a dialog and closes when a link is taken. The
  build stamp's bottom is at 830 of 844.
- **Console and network.** No uncaught errors. The only failed requests
  were the stubs, and the classic task page's
  `GET /api/repos/acme/widgets/github-branches` 404. That is expected:
  the demo has no GitHub.

## Bug 3 — an old `/next/t/<team>/…` link opened Inbox (fixed)

**Reproduction:** open `/next/t/default/tasks`,
`/next/t/default/task/3?tab=chat` or `/next/t/default/settings/access`.
Each lands on `/next/inbox`, through the `**` catch-all. The classic UI
opens `/tasks`, `/tasks/3` and `/settings/access` for the same links
(`ui/src/paths.js`).

**Fix:** a `teamPrefixMatcher` route in `ui-ng/src/app/app.routes.ts`. It
redirects `/t/<team>/<rest>` to `/<rest>` and keeps the query and
fragment. A bare `/t/<team>` goes to Inbox. Checked after the fix in
Chromium and WebKit, at all five sizes.

## Bug 4 — a new session left focus on `<body>` (fixed)

**Reproduction:** on `/next/sessions` press New session, type a name and
press Start session. The chat opens, but `document.activeElement` is
`<body>` at 50, 300, 1000 and 3000 ms. The next keystroke goes nowhere.
This happens at 1440×900 and 390×664.

**Cause:** `/sessions` and `/sessions/:id` are separate routes, so the
navigation in `create()` builds a new page. `create()` then focused the
composer of the old page, which had already been destroyed. A trace of
`focusin` events shows no focus at all after the dialog closed.

**Fix:** `create()` passes `{ grainFocusComposer: true }` as navigation
state. The new page reads it in its constructor, and an effect focuses
the composer as soon as it renders, for up to 15 s. After the fix, focus
is on the composer by 300 ms.

## Bug 5 — viewers were offered create controls and a task composer (fixed)

**Reproduction:** as a role without write scopes (stub 2):

- `/next/` showed the rail's New task button and New menu (all seven
  items) at ≥820 px, and the phone's New task button at 390 and 320;
- the `c` shortcut opened the New task sheet;
- every task page offered the composer and Attach file.

Each of these ends in a call the server refuses. `/next/sessions`
already hid New session and its composer for the same role.

**Fix:**

- **Shell (superseded on merge).** This branch first hid the shell's
  create controls from a viewer. While it was in CI, `main` gained
  `1f3f18bed` ("fix /next/ walkthrough defects"), which gates the same
  controls a different way: it disables them and gives the reason in
  their tooltip. That also follows the design book's "Role-aware" rule.
  The merge took `main`'s `app.component.ts`, so this branch no longer
  changes the shell.
- **Task composer (kept).** `task-detail.component.ts` hides the
  composer when `POST /api/tasks/{id}/comments` is denied, and shows the
  access service's sentence in its place: "Your role grain/viewer does
  not hold tasks:write, which this needs." At 390 that note measures
  13..377. `main` had not changed this.

Before the merge, the walkthrough as a viewer found no write control
visible on any of the eight pages, at 1440 or 390. After the merge, the
New task buttons are present but disabled, and `c` does nothing. An
operator's controls are unchanged.

## Regression coverage

New `ui-ng/e2e/next-shell-session-walkthrough.spec.js`, run with
`make test-e2e-ng`:

- `/next/t/<team>/…` opens the team-less page, keeping the query;
- a new session focuses its composer at 1440 and at 390×664, typing
  goes into it, and Send is on screen;
- as a viewer at 1440, New task is disabled, `c` opens nothing, and a
  task shows the denial note instead of a composer;
- as a viewer at 390, the note fits the screen and no composer shows;
- an operator still has the composer, and New task is enabled.

The `/t/` and focus cases were reproduced on the base build by hand, as
above. With the fixes, the new spec passes together with
`shell.spec.ts`, `next-full-system.spec.js` (all four sizes and themes,
and contrast), `next-dialog-fits-phone.spec.js`,
`next-sessions-canvas.spec.js`, `next-task-interactions.spec.js` and
`next-inbox.spec.js`. The unit tests for `app.component`, `task/` and
`sessions/` pass too (`vitest`, 123 tests).

## Follow-ups filed

- **A. `/next/` says nothing while the daemon hangs.** With every
  `/api/**` request held, `/next/tasks` kept showing "2 agents running"
  and gave no notice for 25 s. Refused connections did raise inline
  errors ("The task list could not be read: Failed to fetch…"), and they
  cleared without a reload once the server answered again. `/next/` has
  no counterpart to the classic disconnected banner, host-mode install
  notice or stale-tasks row.
- **B. `/next/`'s New session dialog.** It has no model picker and no
  Pin to sidebar, both of which the classic dialog has. It still asks
  for "Visible to team (optional)", and session rows still show a team,
  though the product no longer has teams. Its load error also shows the
  raw parse error ("Unexpected token 'b', "boom" is not valid JSON")
  rather than the HTTP status.

## Not covered, and why

- **Live model streams:** there was no Gemini key. The fake agent's
  answers and the optimistic bubbles were exercised, not incremental
  streaming.
- **Classic flows the morning run covered in depth** were not repeated
  here, apart from Boards, `[` and the drawer: pinned items, Customize,
  Staging, the alerts panel, approval cards, stale-tab conflict, Adopt
  shipped defaults.
- **`/next/` Start a fresh chat with handover:** the Director offered
  both choices ("with summary handover" and "clean"), and the clean
  start was taken: the chat log went from 1108 to 212 characters, and
  stayed that way after a reload. The handover path was not taken.
