# Role-gated UI sweep as grain/viewer and grain/contributor (2026-11-07 §3 item 4)

Plan: [`docs/test-plans/2026-11-07.md` §3](../test-plans/2026-11-07.md) item 4. Task 3564, run 2026-11-08 in a grain sandbox. It follows [`2026-11-07-rbac-roles.md`](2026-11-07-rbac-roles.md) (task 3487). That run walked items 1–3, then read 12 routes in a browser and fixed F6 (Inbox answers) and F7 (New task). This run uses each write surface instead of only reading it: it drags, ticks rows, clicks Approve, Grant and Reply, and opens overlays. It also measures the refused controls against an unlimited session, box by box.

## Setup

- Daemon: [`walkthrough.sh`](2026-11-07-rbac-roles/walkthrough.sh) with `HOLD=1`, which leaves `grain daemon` running behind the IAP stand-in proxy on `:18490`. Items 1–3 re-ran on every build used here, with **91/91 PASS** each time (base `main` at `cd952ada`, then the fixed build merged with `main` at `bc33a591`).
- People: `vic@example.com` is `grain/viewer` (a `user:` entry). `dana@example.com` is `grain/contributor` (via `group:devs@example.com`). grain ships no `grain/developer`; see O3 in the earlier report. Each person signs in at `/_iapsim/login?email=…`, so every request carries a freshly signed IAP assertion.
- Fixtures: [`fixtures.sh`](2026-11-07-rbac-roles-ui/fixtures.sh) adds what items 1–3 leave out, using the access code: a kanban board, a template, a suite of it and a schedule.
- No run on a held daemon ever parks, so there is no live Reply or Grant card. The Director session (task 1) is `awaiting_reply` but is kept out of the Inbox. On the pages that need them, [`sweep.mjs`](2026-11-07-rbac-roles-ui/sweep.mjs) answers two real proposed tasks' `GET`s as parked: #6 on a question and #5 on an access request. Only the `GET`s are rewritten, so every Reply, Grant and drag still reaches the real server, and the real server's answer is what is recorded.
- Sweep: [`sweep.mjs`](2026-11-07-rbac-roles-ui/sweep.mjs) in Playwright's Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). It covers 17 visits per person at 1440×900 and 390×844, light and dark: **136 visits per build**, plus 34 baseline visits by an unlimited access-code session (1440 and 390, light). [`summarize.mjs`](2026-11-07-rbac-roles-ui/summarize.mjs) builds the tables.

| Visit | What it does |
|---|---|
| `/` | Drags the first row onto the second (HTML5 drag). |
| `/` bulk | Ticks two rows and reads the bulk selection bar's enabled and refused controls. |
| `/boards/<kanban>` | Drags the first card onto the second. |
| `/tasks/<proposed>` | Reads the task pane. |
| `/tasks/<access request>` | Clicks Grant on the access request card. |
| `/new` | Reads the New Task drawer. |
| `/inbox` | Approve on a proposal; Reply then Grant on the access request; Reply and Send on the question. |
| `/repos`, `/processes` | Read. |
| Template, schedule, suite | Each one opened from `/processes`. |
| `/settings`, `/settings/access`, `/system` | Read. |

Clicks run once per person, at 1440 light. Every other visit only reads, apart from ticking rows and opening overlays.

Each visit records:

- every 4xx/5xx, after the action and an idle of 3 s with the page's own polling running;
- error toasts and console errors;
- empty bars: a toolbar, an actions row or a page-header slot drawn with nothing visible in it, excluding bars the unlimited session sees too;
- refused-control wrappers (`.access-gate`) that are not the size of their control, or sit off their row;
- every uniquely named button's box against the unlimited session's on the same page;
- a 25-press Tab walk: focus lost to `<body>` mid-page, landing off-screen, or landing on something invisible.

## Findings

| # | Finding | Status |
|---|---|---|
| **U1** | **A viewer could drag tasks to reorder them.** On `/`, `vic@` was offered the row drag handles (16 on the demo, 5 here). Dropping a row sent `POST /api/tasks/reorder`, which needs `tasks:write`. The server answered 403, the page showed the toast "vic@example.com (role grain/viewer) holds no scope tasks:write, which POST /api/tasks/reorder needs …", and the console logged a 403. Kanban columns use the same `onReorder`. | **Fixed.** `App.jsx` withholds `onReorder` without `tasks:write` (`canReorderTasks`), as `canMoveBoards` already does for boards. The list and the board then draw no handles, so no gutter is left empty; this is the layout a sorted list already has. The contributor keeps the drag; dana's drop was not refused. |
| **U2** | **Refused Admission and Hosting pushed the task pane down 24px.** In the action column (a `Stack`), both selects carry `sx={{ mt: 1, mb: 0.5 }}`. A `Stack` zeroes its direct children's margins, so ungated those margins did nothing. Once gated, the wrapper is the Stack's child and the margins apply inside it. Each wrapper was 12px taller than its select, so Run now, Close, Add goal, Change owner, Open and Add grant… sat 24px lower for every viewer (1440 and 390, both schemes). | **Fixed.** The dead margins are gone. `AccessGate.jsx` now says a gated control must carry no margin of its own. |
| **U3** | **Refused buttons did not fill their slot.** (a) The template overlay at 390px: when "New task from this template" wraps onto two lines, the footer row stretches its items to 61px. An allowed Save stretches with them; a refused Save stayed 37px tall at the top of its wrapper. (b) The suite overlay's "+ New template" mirrors its margin on the wrapper (`wrapperStyle`). The button also kept its own margin, so the margin counted twice. The inline wrapper's line box then added 6px more. The button sat 6px low, and Run mode, Number of times and the footer sat 14px low. | **Fixed.** The wrapper's child now grows to fill it (`.access-gate > *` in `style.css`). Where `wrapperStyle` carries the control's margin, the refused control drops its own (`AccessGate.jsx`). The suite wrapper is block-level, so its margins collapse as the button's do. |
| **U4** | **Grant and Deny on an access request were offered to every role.** The task pane's access request card, and the Inbox's (reached through Reply, which a contributor holds), showed **Grant all** and **Deny** enabled. Both post `/api/tasks/{id}/access-request/{grant,deny}`, which needs `capabilities:admin`. A viewer's click on the task pane, and a contributor's on the task pane and in the Inbox, got a 403 toast and a console error. Nothing was granted. | **Fixed.** `PendingAccess.jsx` wraps both in `AccessGate scope="capabilities:admin"`. Both stay on one row, Grant first, and the tooltip names the scope. |
| O1 | `GET /api/repos/acme/app/github-branches` answers 404 on the template overlay at 390px, for both roles. The fixture repo `acme/app` is not on GitHub. This is not a role refusal, and nothing is shown to the person. | Recorded. |
| O2 | Content differences, not layout: some boxes still move against the unlimited session, for reasons the page means. A viewer's `/settings` shows the 68px "Read-only for you. Needs connections:write…" banner, so New connection, Open and Save run locations sit 84–104px lower. `/settings/access` at 390 says "Your role: grain/viewer — …" above Log out, which puts Log out 12px lower, and draws "Your role does not include access:write" where the Remove buttons and the add form are. After the contributor opens the Inbox's responder, the rows below it move down. | Behaves as designed. No empty bar or empty slot is left. |
| O3 | Kanban drag on the held daemon: kanban cards reorder only within the queued backlog column, and items 1–3 leave no queued task, so no card is draggable there for anyone, including the unlimited baseline. | Covered on `grain demo` instead: its seeded "Board" offers the drag to the access code and, after U1, none to a viewer (`role-gated-actions.spec.js`). |

## Results

Full per-visit tables: [`results-before.md`](2026-11-07-rbac-roles-ui/results-before.md) (build `cd952ada`, `main` before this change) and [`results-after.md`](2026-11-07-rbac-roles-ui/results-after.md) (this branch).

**Before** (`cd952ada`):

| Person (role) | Viewport | Scheme | Visits | 403s | Other 4xx/5xx | Error toasts | Console errors | Empty bars | Gate wrappers off | Boxes shifted vs unlimited | Tab stops off-screen / invisible / lost |
|---|---|---|---|---|---|---|---|---|---|---|---|
| vic@example.com (grain/viewer) | 1440 | light | 17 | 2 | 0 | 2 | 2 | 0 | 4 | 19 | 0 / 0 / 0 |
| vic@example.com (grain/viewer) | 1440 | dark | 17 | 0 | 0 | 0 | 0 | 0 | 4 | 19 | 0 / 0 / 0 |
| vic@example.com (grain/viewer) | 390 | light | 17 | 0 | 1 | 0 | 1 | 0 | 5 | 18 | 0 / 0 / 0 |
| vic@example.com (grain/viewer) | 390 | dark | 17 | 0 | 1 | 0 | 1 | 0 | 5 | 18 | 0 / 0 / 0 |
| dana@example.com (grain/contributor) | 1440 | light | 17 | 2 | 0 | 2 | 2 | 0 | 0 | 11 | 0 / 0 / 0 |
| dana@example.com (grain/contributor) | 1440 | dark | 17 | 0 | 0 | 0 | 0 | 0 | 0 | 7 | 0 / 0 / 0 |
| dana@example.com (grain/contributor) | 390 | light | 17 | 0 | 1 | 0 | 1 | 0 | 1 | 6 | 0 / 0 / 0 |
| dana@example.com (grain/contributor) | 390 | dark | 17 | 0 | 1 | 0 | 1 | 0 | 1 | 6 | 0 / 0 / 0 |

**After** (this branch):

| Person (role) | Viewport | Scheme | Visits | 403s | Other 4xx/5xx | Error toasts | Console errors | Empty bars | Gate wrappers off | Boxes shifted vs unlimited | Tab stops off-screen / invisible / lost |
|---|---|---|---|---|---|---|---|---|---|---|---|
| vic@example.com (grain/viewer) | 1440 | light | 17 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 0 / 0 / 0 |
| vic@example.com (grain/viewer) | 1440 | dark | 17 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 0 / 0 / 0 |
| vic@example.com (grain/viewer) | 390 | light | 17 | 0 | 1 | 0 | 1 | 0 | 0 | 1 | 0 / 0 / 0 |
| vic@example.com (grain/viewer) | 390 | dark | 17 | 0 | 1 | 0 | 1 | 0 | 0 | 1 | 0 / 0 / 0 |
| dana@example.com (grain/contributor) | 1440 | light | 17 | 0 | 0 | 0 | 0 | 0 | 0 | 7 | 0 / 0 / 0 |
| dana@example.com (grain/contributor) | 1440 | dark | 17 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 0 / 0 / 0 |
| dana@example.com (grain/contributor) | 390 | light | 17 | 0 | 1 | 0 | 1 | 0 | 0 | 1 | 0 / 0 / 0 |
| dana@example.com (grain/contributor) | 390 | dark | 17 | 0 | 1 | 0 | 1 | 0 | 0 | 1 | 0 / 0 / 0 |

The 1 "other 4xx" and 1 console error at 390 are O1's 404. The boxes still shifted after the fix are O2's content differences.

- **403s, toasts and console errors**:
  - Before: 4 403s, all from clicks (U1 and U4); none came from background polling on any page.
  - After: **0** 403s, 0 error toasts and 0 console 403s across all 136 visits, with every page polling for its idle.
  - The only other error is O1's 404.
- **Empty toolbars**: 0 on both builds. The one bar both sessions show empty (`/settings`, `/settings/access` and `/system` at 1440 have an empty page-header action slot) is the page's own layout and is drawn the same for the access code. No refused or hidden control leaves a bar of its own empty: the bulk bar keeps Open and Clear selection enabled beside its refused actions, and overflows by 0px at 390.
- **Shifted siblings**: before, U2 and U3 moved 6–14 controls on the task pane, template overlay and suite overlay at both widths. After, every refused-control wrapper is the size of its control (0 off, out of up to 18 wrappers per page). Every button the two sessions share sits within 2px of the unlimited one's box, apart from the deliberate content differences in O2.
- **Tab stops**: 7–25 distinct stops per page. Focus never landed off-screen or on an invisible element, and never fell to `<body>` mid-page, on either build. A refused control's wrapper is a stop (`tabIndex=0`) so its tooltip can be read from the keyboard. A disabled button itself never takes focus.
- **What each role is offered** (after):
  - Viewer: no enabled write control on any visited page or overlay. On the bulk bar only Open and Clear selection are enabled, and Approve, Run selected now, Submit, Retry merge, Defer, Undefer, Close, Reopen, Bulk edit…, Attach and Detach are refused. Reply, Grant and Approve are refused in the Inbox, and Grant/Deny are refused on the task pane.
  - Contributor: on the bulk bar, Submit, Defer, Undefer, Close, Reopen and Bulk edit… are enabled; Approve, Run selected now, Retry merge, Attach and Detach are refused. Elsewhere it keeps the drag and Reply (both reached the server unrefused), Defer, Decline, New task and Edit. Approve and Approve all stay refused (`tasks:admin`, F6), as do Grant and Deny (`capabilities:admin`, U4).

## Regressions

`ui/e2e/role-gated-actions.spec.js` (over `grain demo`, viewer role on the page alone):

- *a viewer is offered no reorder handles on the task list*, and *… no drag on a kanban board's cards*. Each first checks that the access code is offered them. U1.
- *a viewer's refused task controls keep the pane's own spacing*: every wrapper in the task pane's action column is its control's size. U2.
- *a viewer's suite New template sits where the allowed one does*: the button and Save are compared, box against box, with the unlimited session. U3b.
- *on a phone › a viewer's refused template buttons fill their footer row*. U3a.
- *a viewer's access request Grant and Deny are disabled, and say why*: one row, Grant first, with the tooltip. U4.

`AccessGate.test.jsx`: a `wrapperStyle` margin goes on the wrapper only.

All six specs fail on the unfixed UI (16 handles offered; wrapper 12px and 24.5px off; Grant enabled; New template 6px off) and pass on this branch. The whole spec passes 14/14 locally.

## Screenshots

[`contact-sheet-before.png`](2026-11-07-rbac-roles-ui/contact-sheet-before.png) and [`contact-sheet-after.png`](2026-11-07-rbac-roles-ui/contact-sheet-after.png) show `/`, the task pane, the task pane's access request card, the Inbox's access request and the template overlay, for both people at both widths and in both schemes. The sandbox's file reader cannot display PNGs, and `LOOK=1` has no Gemini key here. The layout claims above therefore rest on the bounding-box measurements and the spec's assertions, not on a visual read.

## Reproduce

```
HOLD=1 GRAIN=$(pwd)/grain docs/test-runs/2026-11-07-rbac-roles/walkthrough.sh /tmp/rbac &
# after "holding daemon":
docs/test-runs/2026-11-07-rbac-roles-ui/fixtures.sh /tmp/rbac
PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node docs/test-runs/2026-11-07-rbac-roles-ui/sweep.mjs \
  http://127.0.0.1:18490 http://127.0.0.1:14320 "$(cat /tmp/rbac/data/local-api-token)" /tmp/shots \
  vic@example.com=viewer dana@example.com=contributor
node docs/test-runs/2026-11-07-rbac-roles-ui/summarize.mjs /tmp/shots/results.json
(cd ui && npx playwright test e2e/role-gated-actions.spec.js)
```

`grain` must embed a built frontend (`make frontend`, or `npm run build` in `ui/`, before `go build ./cmd/grain`). Without one, the daemon serves `placeholder.html` and every page check passes vacuously. The walkthrough rewrites the artifacts under `2026-11-07-rbac-roles/` as it runs; `git checkout` them afterwards.
