# Responsive navigation and live-session UI walkthrough — 2026-11-11 (task 4305)

- **Date:** 2026-11-11, 08:50–09:45 UTC, one run.
- **Task:** task 4305, branch
  `grain/098073e4/task-4305-walk-through-responsive`, base `main`.
- **Commit tested:** `3cb32134c` ("Merge pull request #1196"), build stamp
  `v0.0.0-20261111084610-3cb32134cc70 · 3cb3213 · 2026-11-11 08:46Z`. The
  fix below was then rebuilt into the same commit and checked again on a
  fresh store.
- **Earlier runs in this series:** most recently
  `2026-11-09-responsive-navigation-live-sessions.md` (task 3721). This run
  walked the same area again on the current `main` with new, independent
  drivers. Since 11-09, `main` added the debounced stale task list
  (`af7858e7`), lazily loaded task threads (`bec18394`), and role gating of
  the task list, Comment and Edit fields (`ecf9e8e4`).
- **Result:** one fault was found and fixed (Bug 1: a viewer was offered
  grant, goal and owner edits on the task pane). Everything else listed
  below passed in Chromium and WebKit.

## Environment

- Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0; Playwright
  1.62.1 from `ui/package-lock.json`. Headless Chromium and WebKit
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC.
- Built with `cd ui && npm ci && npm run build`, then
  `go build -o /var/tmp/grain ./cmd/grain`.
- Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file
<file> -observability -staging -config-change -host-mode-install
-merge-rules -mechanical-port`. `TMPDIR` pointed at a fresh scratch
  directory on every start, so each store was a throwaway embedded SQLite
  file. For the before/after screenshots, the unfixed binary also ran on
  `127.0.0.1:8421` over its own store.
- There was no `/home/debian/.gemini-api-key`, so no model was called. With
  no key, `LOOK=1` could not describe screenshots either. Layout was
  therefore asserted through live DOM geometry: `getBoundingClientRect`,
  `elementFromPoint` hit tests, `scrollWidth` against `innerWidth`, and
  `window.scrollY`. Contrast was checked with `@axe-core/playwright`'s
  `color-contrast` rule.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each browser context signs in through `POST
/api/auth/access-code` (name `alice@example.com`) and then acts only
  through visible controls (rail and drawer items, tabs, header buttons,
  menus, composer). Every page recorded:
  - uncaught page errors and console errors;
  - failed requests and `/api/` responses of 400 or more;
  - every native dialog, with its text and whether it was accepted;
  - non-GET `/api/` bodies where a step depended on them.

  The REST API (bearer access code) was used only to read back what a
  gesture had persisted, and to create one session for the second session
  pass.

### Viewports

| Name          | Size     | Context                       |
| ------------- | -------- | ----------------------------- |
| Desktop       | 1440×900 | fine pointer, keyboard        |
| Laptop        | 1024×768 | fine pointer, keyboard        |
| Narrow tablet | 720×900  | header overflow case          |
| Tablet        | 820×1180 | `hasTouch`                    |
| Phone         | 390×844  | `hasTouch`, `isMobile`        |
| Narrow phone  | 320×568  | `hasTouch`, `isMobile`        |
| Rotated phone | 844×390  | resized from 390×844          |
| Live resize   | 1440→390 | then back to 1440 in one page |

## Stubs

The server, store, built frontend, browsers and gestures were real
throughout. Only these boundaries were intercepted, each with a Playwright
`page.route` on that one page:

1. **A hung daemon.** Every `GET /api/**` was held for 60 s and then
   aborted. This is a daemon that accepts connections but never answers.
   The routes were then removed, so the server answered again without a
   reload.
2. **A viewer role.** `GET /api/config`'s `access` was rewritten to
   `grain/viewer` scopes. This is the same stub
   `ui/e2e/role-gated-actions.spec.js` uses. An access-code session always
   holds the code's full power, so this stub tests only what the UI offers.
   `pkg/ui`'s route-scope tests cover the server's refusals.
3. **The session manager still starting.**
   `GET /api/console-sessions` answered
   `503 {"error":"console session manager is still starting"}` on a cold
   load of `/sessions`, then passed through after Retry.
4. **A slow session start.** `POST /api/console-sessions` was held behind
   a latch, so the optimistic first message, a queued follow-up and an
   unsent draft could be observed before the session existed.
5. **A failed send.** The first `POST /api/tasks/*/comments` from a session
   answered `500 {"error":"store unavailable"}`. Later sends passed
   through.

Nothing else was stubbed. A WebKit shell run overlapped a server restart,
so some `Connection refused` lines in that run's log were caused by the
restart, not by the UI.

## Steps and outcomes

### 1. Navigation sweep (Chromium and WebKit)

This pass covered 250 checked steps per engine across 1440, 1024, 820, 390
and 320 widths. All navigation went through the real rail at 1440, 1024
and 820, and through the drawer (Open navigation) at 390 and 320. After
every step the driver asserted:

- no horizontal page overflow;
- no interactive control outside the viewport, unless it sat inside a
  scroll container;
- on desktop and tablet, a single-row header (77 px) whose Alerts, System
  and Settings buttons are inside the viewport and win their own hit test;
- on a phone, the bar's menu, Settings and New task inside the viewport,
  with the page title heading under the bar;
- no `Admin mode`, `All teams` or team switcher text anywhere.

| Area                                                               | Outcome                                                                                                                                                                                                                           | Verdict |
| ------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Inbox, Tasks, Running, Blocked, Boards, Sessions, Processes, Repos | Crumbs read `inbox`, `tasks`, `boards`, `sessions`, `processes`, `repositories`. Phone headings read Inbox, All tasks, Running, Blocked, Boards, Sessions, Processes / Suite runs, Repos.                                         | PASS    |
| Task detail and new task                                           | The first task row opens `task 24`. The rail's + New task opens `new task`. The header's globals sit under the modal pane, which is expected.                                                                                     | PASS    |
| Boards                                                             | Board (kanban), Timeline (gantt), Report and Tree each open with crumbs `boards/<name>` and no overflow.                                                                                                                          | PASS    |
| `acme/widgets` tabs                                                | Overview, CI, Merge queue, Agents, Branches, Releases and Advanced. Crumbs read `repositories/acme/widgets/<tab>`.                                                                                                                | PASS    |
| Settings, 20 sections                                              | Desktop uses `settings-nav-*`. Tablet uses the `settings-section-select` picker. Phone uses the section list with All settings to go back. Crumbs read `settings/<section>`, including `settings/hub` and `settings/config repo`. | PASS    |
| System, 8 tabs                                                     | Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics and Power, from the tab strip and from the phone's System list with All of System to go back. Crumbs read `system/<tab>`.                                                 | PASS    |
| Back, Forward, reload                                              | Back from Power gives Metrics, Forward gives Power, and a hard reload keeps `system/power`.                                                                                                                                       | PASS    |
| Console                                                            | No page errors, console errors or failed requests in either engine.                                                                                                                                                               | PASS    |

### 2. Header rail and overflow (720×900)

| Step              | Outcome                                                                                                                                                                                                | Verdict |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------- |
| Fit               | On `/` and `/repos`, More page actions shows and the header stays 77 px, one row. `/boards` fits without it.                                                                                           | PASS    |
| Open              | `aria-expanded="true"`. The slot drops below the rail (488×109) and is on top at its own hit test. The wrapped actions (count, search, Filters (1), Backlog order, Contact running (2)) are reachable. | PASS    |
| Escape            | Closes the slot and hands focus back to `page-header-more`.                                                                                                                                            | PASS    |
| Outside press     | Closes the slot.                                                                                                                                                                                       | PASS    |
| Tab               | Tabbing from the rail through the user tile and build stamp into a wrapped action opens the slot by itself. Tabbing on past it closes the slot.                                                        | PASS    |
| Legacy team links | `/t/acme/repos`, `/t/platform/settings/models`, `/t/core/inbox`, `/t/core/tasks/1` and `/t/x/system/metrics` open their team-less pages.                                                               | PASS    |

### 3. Rail

| Step             | Outcome                                                                                                                                                             | Verdict |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Collapse sidebar | 232 → 60 px, and still 60 after a reload. `[` expands it again (60 → 232). Typing `[[` into the page's search field does not toggle it.                             | PASS    |
| User tile        | `GET /api/whoami` gives `alice@example.com (access code)`. Collapsed, it is an icon with the tooltip `alice@example.com (access code)`. It opens Settings → Access. | PASS    |
| Build stamp      | Opens `/settings/upgrade`. It is not drawn while the rail is collapsed, which is by design (`Sidebar.jsx`).                                                         | PASS    |
| Create options   | New task, New session, New board.                                                                                                                                   | PASS    |
| Customize        | Show CI, then Save, adds CI to this tab's rail and to a second open tab without a reload. CI opens `/ci` with no overflow.                                          | PASS    |
| Staging          | Lists `docs-preview` (`/staging/docs-preview/`) and `grain-staging` (`http://127.0.0.1:4280/`). Pinning `docs-preview` survives a reload.                           | PASS    |
| Session lit      | The new session's rail entry is selected while it is open. The rail's labels are lowercased by CSS.                                                                 | PASS    |

### 4. Phone bar and drawer (390×844, 320×568)

| Step                   | Outcome                                                                                                                                                                                                                                                                                                                                                  | Verdict |
| ---------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Live resize 1440 → 390 | The rail leaves, the bar's Open navigation appears, and there is no overflow. Going back to 1440 restores the rail.                                                                                                                                                                                                                                      | PASS    |
| Drawer                 | `aria-modal="true"`, 232 px wide. Its foot holds the System row, the user tile at 753–797 and the build stamp at 807–830 of 844. The stamp is a `noWrap` ellipsis with the full text in its title. At 320×568 the list scrolls (892/297) and the tile and stamp stay on screen (477–521, 531–554). A wheel over the drawer leaves `window.scrollY` at 0. | PASS    |
| Stays open             | It stays open while the create menu is open and after Escape from it. It also stays open under Customize sidebar and after Escape from that.                                                                                                                                                                                                             | PASS    |
| Closes                 | Keyboard Enter on Inbox navigates and closes the drawer, and focus returns to Open navigation. The user tile opens `/settings/access` and closes the drawer.                                                                                                                                                                                             | PASS    |
| Alerts panel           | 16..374 of 390, so off neither edge. Escape returns focus to the bell.                                                                                                                                                                                                                                                                                   | PASS    |
| Rotation               | At 844×390 and 320×568: no overflow, no clipped controls.                                                                                                                                                                                                                                                                                                | PASS    |

### 5. Banners

| Step                               | Outcome                                                                                                                                                                                    | Verdict |
| ---------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------- |
| Hung daemon (stub 1), 1440 and 390 | "This deployment cannot be reached: it accepted the connection…" appears above the host-mode install notice (1440: 0–48 and 48–116; 390: 0–128 and 128–256), full width, with no overflow. | PASS    |
| Recovery                           | When the daemon answers again, the banner clears without a reload.                                                                                                                         | PASS    |

### 6. Sessions (Chromium and WebKit)

| Step                                        | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                 | Verdict |
| ------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| New session from Create options             | Opens `/session/new` with the composer (`Message`) focused, plus Session name, the model picker and Pin to sidebar.                                                                                                                                                                                                                                                                                                                     | PASS    |
| First send while the start is held (stub 4) | The first message shows at once as a `Sending…` bubble. A second send queues as its own `Sending…` bubble. An unsent draft is kept in the box.                                                                                                                                                                                                                                                                                          | PASS    |
| Session opens                               | It opens at `/session/walk-chromium-a` (WebKit: `walk-webkit-a`), and the unsent draft (`unsent draft text`) is in its composer. The requests were `POST /api/console-sessions {"title":…,"message":…}`, then `POST /api/tasks/27/comments` for the queued send. The task's comments read greeting, first, second. Both messages are still there after a reload, and the session is named, pinned (`grain.pinned`) and lit in the rail. | PASS    |
| New session from `/sessions` at 320×568     | Send is at y 470–510 of 568. Sending opens `/session/walk-chromium-phone`, with no overflow and the composer on screen. Scrolling leaves `window.scrollY` at 0 (Chromium; WebKit's mobile emulation has no wheel).                                                                                                                                                                                                                      | PASS    |
| `/director`, `/session/director`            | Both open Director (`sessions / Director`) with its transcript and task tree.                                                                                                                                                                                                                                                                                                                                                           | PASS    |
| Deep link to a deleted or unknown session   | `/session/does-not-exist-123` says "There is no session … It may have been deleted; pick another below." above the list, not another session's chat.                                                                                                                                                                                                                                                                                    | PASS    |
| `/sessions` on 503 (stub 3)                 | "The sessions could not be loaded (console session manager is still starting)." with Retry. Retry recovers to the list.                                                                                                                                                                                                                                                                                                                 | PASS    |
| Delete a pinned session                     | The confirm "Delete session "Draft carry"? This removes the session from this deployment." was declined first: nothing changed and focus stayed on Delete session. Accepted, it removes the session, drops its `grain.pinned` entry and clears the rail. A deep link to it then lists the sessions.                                                                                                                                     | PASS    |
| Failed send (stub 5)                        | "Message not sent. It is back in the message box." The composer keeps focus. Enter resends it exactly once.                                                                                                                                                                                                                                                                                                                             | PASS    |
| Back-to-back sends                          | Two sends in a row land in order after a reload.                                                                                                                                                                                                                                                                                                                                                                                        | PASS    |
| Markdown                                    | A table in a message renders as a `<table>` with no overflow.                                                                                                                                                                                                                                                                                                                                                                           | PASS    |
| Director live controls                      | Edit Director capabilities shows "No run is open on this session, so a change saved here takes effect on the next message you send." The grants include `grain/config · write` with its base. The warning for `networkAccess: "none"` (effective `open`) is shown.                                                                                                                                                                      | PASS    |
| Escape in Add grant…                        | Escape closes only the menu. The capabilities region stays open and focus returns to Add grant….                                                                                                                                                                                                                                                                                                                                        | PASS    |
| Start a fresh chat                          | Offers "Close this chat and start a fresh one" and "Start fresh with summary". The first sends `POST …/restart {"carryHandover":false}` and opens an empty chat on the same session.                                                                                                                                                                                                                                                    | PASS    |
| Reduced motion                              | With `reducedMotion: "reduce"`, the typing dots have `animation-name: none`.                                                                                                                                                                                                                                                                                                                                                            | PASS    |
| Contrast                                    | `color-contrast` found 0 violations on the session page, `/sessions`, `/` and `/inbox`, in light and in dark.                                                                                                                                                                                                                                                                                                                           | PASS    |

### 7. Role-gated rendering (stub 2)

The rail's New task is disabled for a viewer, as are the pane's Edit,
Comment, Approve, Defer, Pause, Run now, Close and Edit fields. Hovering
one gives "Needs tasks:write, which your role grain/viewer does not grant."
The task pane's grant lines, goal and owner, however, were still live
(Bug 1).

## Bug 1 — a viewer was offered grant, goal and owner edits on the task pane (fixed)

**Reproduction:** sign in, stub `/api/config`'s `access` to `grain/viewer`
(stub 2), and open `/tasks/1` at 1440×900 or 390×844. Before the fix, these
were all enabled:

- Remove `api:self-debug`, Remove `github › read-issues`, Remove
  `grain › ci`, Remove `grain › source`;
- Add another self-debug, and Add grant…;
- Add goal, and the owner's Change (`Change owner`).

Each is a write the server refuses that role:

- `POST /api/tasks/{id}/capabilities` needs `capabilities:admin`, or
  `capabilities:detach` to remove a grant;
- `PUT` and `DELETE /api/tasks/{id}/goal` need `tasks:write`;
- `PATCH /api/tasks/{id}` (owner, and the repo lines) needs `tasks:write`.

The rest of the pane had been gated by `ecf9e8e4`, but these controls were
not.

**Fix:**

- `GoalSection.jsx` and `Declared.jsx` wrap Add goal / Edit goal / Remove
  goal and Change owner in `AccessGate scope="tasks:write"`. They are
  disabled with the reason, like the pane's actions.
- `CapabilityToggles.jsx` passes `disabled` to `GrantsPicker` when the
  principal holds none of `tasks:write`, `capabilities:admin` and
  `capabilities:detach`. The grants stay listed (with About), but there is
  no Remove and no Add grant….

Afterwards, a viewer's enabled controls on the pane are ← Back, Prompt,
Provenance, Attach files and the About buttons, and no write is sent. The
operator's pane is unchanged.

**Regression coverage:**

- `ui/src/components/detail/CapabilityToggles.test.jsx` gains a viewer case
  and an operator case on the full `DetailOverlay`. The viewer case fails
  on the base and passes with the fix.
- `ui/e2e/role-gated-actions.spec.js` gains "a viewer is offered no grant,
  goal or owner edits on a task". It runs against the real demo and checks
  that the AccessGate wrappers fill their controls. The whole spec passes:
  16/16 in Chromium.
- Before and after screenshots of the viewer's `/tasks/1` were taken at
  1440 and 390, in light and dark. They are attached to the task's closing
  note and are not committed.

## Not covered, and why

- **Pause and Resume on a task-bound interactive chat.** The demo's only
  interactive tasks are standing sessions. `/tasks/<id>` for one redirects
  to `/session/<id>`, which offers Stop, not Pause. `TaskChat`'s
  Pause/Resume is drawn only for a chat with neither `onSend` nor `onStop`,
  and this store has none.
- **Verified identity.** The phone drawer's `Signed in as <email>` with Log
  out underneath needs an IAP verifier, which this run did not stand up.
  The access-code tile (`alice@example.com (access code)`) was checked
  instead. `ui/e2e/phone-signed-in-logout.spec.js` and
  `iap-header-logout.spec.js` cover the verified path.
- **Cards and runs that need agent rows.** Inline `request_access`,
  local-script and config-repo approval cards in a chat, the next-run
  notice with Apply now while a run is in flight, Adopt shipped defaults,
  and a stale-tab conflict retry all need rows a run or a stub writes. The
  11-09 run stubbed them; this run did not repeat that.
- **The stale task-list retry row (`phone-tasks-stale`).** Not re-walked.
  It has its own spec (`ui/e2e/phone-tasks-stale.spec.js`), which is
  unchanged.

## Notes

- `GET /api/repos/acme/widgets/github-branches` answers 404 on every task
  pane. The demo has no GitHub branch lister, and earlier reports record
  this as expected.
- In the viewer's pane, Attach files stays enabled. It only stages a file
  for a comment, and Comment itself is gated, so nothing is written.
