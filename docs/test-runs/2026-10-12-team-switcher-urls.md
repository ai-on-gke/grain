# Walkthrough: team switcher, team URLs and no "All teams" (2026-10-12 plan §8)

**Plan:** `docs/test-plans/2026-10-12.md` §8 (on `candidate`; read with `git show origin/candidate:…`)
**Target:** `main` at `f3dfcaa5c`. The feature (`8c62f4279`) and its follow-ups (`35465dd70`, `c63137592`,
`72ea1641a`) are all on `main`. They are on `candidate` too by now (`git branch -r --contains`), but
this run was against `main`.
**Run by:** task 3291 (origin task 3209), 2026-10-13

## Environment

- `grain demo -addr 127.0.0.1:0 -open=false` on its embedded SQLite store, built from this tree by
  `ui/e2e/global-setup.js` (one demo per Playwright worker, `ui/e2e/fixtures.js`). Readiness is
  `GET /api/config` from the fixture, and `/api/sandboxes` answered `{"enabled":false}` on the
  ad-hoc demo used for the API probes.
- UI built with `cd ui && npm ci && npm run build` and served by the demo (`pkg/ui/static`).
- Playwright 1.x, Chromium headless shell 151 (`npx playwright install chromium`; the sandbox needed
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt` for the browser download
  through the package-cache proxy).
- Identities are the demo's header identity (`localStorage["grain.username"]` → `X-Grain-User`).
  Non-admin contexts send no `X-Grain-Admin-Mode` (`test.use({ extraHTTPHeaders: {} })` or a context
  with `extraHTTPHeaders: {}`). Admin-mode-on contexts send `X-Grain-Admin-Mode: on`.

### Stubs

None. Nothing in grain's APIs or React was mocked. The walkthrough touches no external boundary:
it uses no GitHub, no model provider and no live session. Teams, tasks, schedules, boards and executor
tokens were all created through grain's own HTTP API on the demo.

## Setup (step 1)

| Who | Teams | Notes |
| --- | --- | --- |
| `solo<stamp>` | Red (member) | on one team |
| `duo<stamp>` | Red (member), Green (admin) | on two teams |
| `sysadmin` | default only | in `adminAccounts`; on none of Red/Green/Blue |
| `alice` | creator of Red/Green/Blue | in `adminAccounts`. Creating a team makes its creator an admin of it, so alice is **on** every team. The "team not mine" rows below use `sysadmin` for that reason. |

Three teams `red-<stamp>`, `green-<stamp>`, `blue-<stamp>`. One task on each (ids 13 = Blue, 14 = Green,
15 = Red), plus a schedule and a board on Red.

## Results

### Step 2: keyboard only (`duo`, Chromium)

Focus order: the team picker (`combobox "Team"`) is the first Tab stop on the page. **Enter** or
**Space** opens it with focus in **Search teams**. **ArrowDown/ArrowUp** step through the options and
wrap. Enter in the search field takes the first match. Tab goes Search → each option → **New Team**.
**Escape** closes it back onto the picker. **New Team** opens "Create New Team" with focus in its
first field: 13 Tabs reach **Save**, Escape closes it with focus back on the picker, and Save creates
the team and lands on `/t/<new>` with focus on the picker. There was no focus trap outside the dialog.
The dialog's own trap is MUI's and is correct.

Found (all fixed here, F1–F3 below):

- Picking a team with Enter dropped focus onto `<body>`. That happened on a focused option and on the
  first match from the search field.
- Tab past **New Team** moved focus into the page behind and left the list open over it
  (`aria-expanded="true"`).
- ArrowDown on the closed picker did nothing. Enter and Space open it.

### Step 3: legacy and foreign URLs

Each URL was opened in a fresh context after `/settings`, then `history.back()`. `history.length` was
3 every time (`about:blank`, `/settings`, the landing page), so every correction **replaced** the
entry, and Back reached `/settings` in every row. **No loops.**

| User | URL | Lands on | Notice |
| --- | --- | --- | --- |
| solo | `/teams/red/tasks` | `/t/red/tasks` | — |
| solo | `/?team=green` | `/t/red/tasks` | `no team "green" you are a member of` |
| solo | `/tasks/13` (Blue's) | `/t/red/tasks` | `no task 13` |
| solo | `/tasks/14` (Green's) | `/t/red/tasks` | `no task 14` |
| solo | `/t/nope` | `/t/red` | `no team "nope" you are a member of` |
| solo | `/t/blue` | `/t/red` | `no team "blue" you are a member of` |
| solo | `/t/blue/tasks/13` | `/t/red/tasks` | `no team "blue" you are a member of` |
| duo | `/teams/red/tasks` | `/t/red/tasks` | — |
| duo | `/?team=green` | `/t/green/tasks` | (Green's setup banner only) |
| duo | `/tasks/13` (Blue's) | `/t/green/tasks` | `no task 13` |
| duo | `/tasks/14` (Green's) | `/t/green/tasks/14` | — |
| duo | `/t/nope` | `/t/green` | `no team "nope" you are a member of` |
| duo | `/t/blue` | `/t/green` | `no team "blue" you are a member of` |
| duo | `/t/blue/tasks/13` | `/t/green/tasks` | `no team "blue" you are a member of` |
| sysadmin, Admin mode on | `/teams/red/tasks` | `/t/red/tasks` | — (role chip "system admin") |
| sysadmin, Admin mode on | `/?team=green` | `/t/green/tasks` | — |
| sysadmin, Admin mode on | `/tasks/13` | `/t/blue/tasks/13` | — (opens in its own team) |
| sysadmin, Admin mode on | `/t/nope` | `/t/default` | `no team "nope" you are a member of` |
| sysadmin, Admin mode on | `/t/blue`, `/t/blue/tasks/13` | stay | — |
| sysadmin, Admin mode off | `/teams/red/tasks`, `/?team=green`, `/t/blue`, `/t/blue/tasks/13` | `/t/default[/tasks]` | `no team "<x>" you are a member of` |
| sysadmin, Admin mode off | `/tasks/13` | `/t/default/tasks` | `no task 13` |
| sysadmin, Admin mode off | `/t/nope` | `/t/default` | `no team "nope" you are a member of` |

(`<stamp>` suffixes dropped from the team ids above.) A link to another team's task opens in that team
for whoever can read it, and says "no task N" to whoever cannot. It does not reveal that the task
exists. The setup-steps banner ("Team X: 2 setup steps left …") also showed on every team page,
because the teams have no git host or model connection. That banner is expected.

### Step 4: no storage, and stored team `all` (`duo`)

| `grain.selectedTeam` | URL | Lands on | Stored afterwards |
| --- | --- | --- | --- |
| (none) | `/` | `/t/green/tasks` (first team) | `green` |
| (none) | `/tasks/14` | `/t/green/tasks/14` | `green` |
| (none) | `/tasks/15` | `/t/red/tasks/15` | `red` |
| `all` | `/` | `/t/green/tasks` | `green` (`all` dropped) |
| `all` | `/tasks/14` | `/t/green/tasks/14` | `green` |
| `all` | `/tasks/15` | `/t/red/tasks/15` | `red` |

No page errors, and no request carried `team=all`.

### Step 5: switching team on each page (`duo`, Red → Green)

| Page | Before | After the switch | Back |
| --- | --- | --- | --- |
| Tasks (task 15 open) | `/t/red/tasks/15` | `/t/green` (Home) | `/t/red/tasks` |
| Inbox | `/t/red/inbox` | `/t/green` (Home) | `/t/red/inbox` |
| Boards (board open) | `/t/red/boards/board-5` | `/t/green` (Home) | `/t/red/boards/board-5` |
| Schedules (schedule open) | `/t/red/schedules/sched-3` | `/t/green` (Home) | `/t/red/schedules` |
| CI | `/t/red/ci` | `/t/green` (Home) | `/t/red/ci` |
| Repos (acme/widgets open) | `/t/red/repos/acme/widgets` | `/t/green` (Home) | `/t/red/repos/acme/widgets` |
| Templates | `/t/red/templates` | `/t/green` (Home) | `/t/red/templates` |

Open items close: nothing of Red's is left open on Green. **The page kind is not kept.** Every
switch goes to the new team's Home. That is deliberate since `f16e0a153` ("team switcher goes to
the team home", task 3232), which came after the commits the plan describes, so the plan's "page
kind is kept" is out of date rather than a bug (finding F4). An open task or schedule is a modal
dialog, so the top bar cannot be reached until it is closed. In the task and schedule rows above, the
item was closed with Escape before the switch.

### Step 6: viewports (`duo`)

- **375×812** (phone shell): the drawer is a column (`flex-direction: column`), with the top bar at
  the top (232×93) and the rail under it (232×719). The picker is 44 px tall at the top of the
  drawer. Its list opens under it, inside the drawer (245×88). Picking a team navigates and closes the
  drawer. The rail's footer is **below the fold**: System starts at y≈780 and ends at y≈824, and
  Metrics, the signed-in user and the build line are at y≈837–921 on an 812-px screen. The rail
  scrolls (`overflow-y: auto`, 860 px of content in 719), so they are reachable by scrolling the
  drawer, but they are not on screen when it opens. Filed as F5.
- **768×1024** (tablet, no phone shell): the top bar sits above the page, and the picker (124×40) and
  its list (245×65) work as on desktop. System is on screen at y≈863.

### Step 7: Executors and deployment-wide repo defaults

- **System → Executors** (`/admin/executors`, alice with Admin mode on) lists the tokens issued for
  Red, Green and Blue (`POST /api/executors/tokens` with each team): all three rows are shown.
- **Repo page switch** "Edit deployment-wide defaults (every team inherits these)" on
  `/t/green/repos/acme/widgets`: shown to alice with Admin mode on (1). Not shown to alice with Admin
  mode off (0), to duo, Green's team admin (0), or to solo (0, redirected to Red's page).
- **Edits reach every team**: with the switch on, the global "Prompt extension for acme/widgets" was
  set (`PUT /api/repos/acme/widgets/prompt-extension` → 200). Each team's own box then shows it as
  the inherited text: Green (switch off again, same page), Red and Blue (alice, Admin mode on), Blue
  (alice, Admin mode off), Green and Red (duo), Red (solo).
- The switch's state is kept per repo in `sessionStorage` (`grain.repoDeploymentWide.<repo>`), so it
  was still on after a team switch and a reload in the same tab. This is by design (RepoPage.jsx).

## Findings

| # | Finding | Severity | Disposition |
| --- | --- | --- | --- |
| F1 | Picking a team from the keyboard (Enter on an option, or Enter in the search field) leaves focus on `<body>`. The next Tab restarts from the top of the page. | a11y, medium | **Fixed** |
| F2 | Tab past **New Team** moves focus into the page and leaves the team list open over it. | a11y, low | **Fixed** |
| F3 | ArrowDown/ArrowUp on the closed picker does not open it, unlike a combobox's usual keys. Enter and Space do open it. | a11y, low | **Fixed** |
| F4 | Plan §8 says switching keeps the page kind. Since `f16e0a153` (task 3232) it goes to the team's Home on purpose. | plan out of date | Recorded; the plan doc lives on `candidate` |
| F5 | At 375×812 the rail footer (System, Metrics, user, build line) is below the fold in the phone drawer, reachable only by scrolling it. | layout, low | Filed with propose_task (design call: pin the footer, or accept scrolling) |

### Reproductions

- **F1**: as a member of two teams, open `/t/<a>/tasks`, focus the Team picker, press Enter, type part
  of the other team's name, then ArrowDown and Enter. `document.activeElement` is `<body>`.
  Regression: `ui/e2e/team-switcher-keyboard.spec.js` ("a team picked from the keyboard leaves focus
  on the picker") and `TopBar.test.jsx` ("keeps the keyboard on the picker …").
- **F2**: open the picker with Enter and press Tab once per control (Search, each team, New Team) and
  once more. The listbox is still visible and `aria-expanded="true"`. Regression: the same spec
  ("Tab out of the open picker closes it").
- **F3**: focus the closed picker and press ArrowDown. `aria-expanded` stays `false`. Regression: the
  first test of the spec opens the picker with ArrowDown.
- **F5**: at a 375×812 viewport as `duo`, open `/t/red/tasks`, tap **Open navigation**, then measure
  `getBoundingClientRect()` of the rail's System button: `bottom` ≈ 824 > 812.

## Fixes

`ui/src/components/TopBar.jsx`:

- After an option is picked (Enter included, through ButtonBase), focus returns to the picker on the
  next frame, but only if it fell to `<body>`. In the phone drawer, the pick closes the drawer, and
  the drawer hands focus back itself. (F1)
- A Tab keypress inside the list closes the list once focus has landed outside both the list and the
  picker. A first version closed the list on any `blur`. That broke picking from the phone drawer:
  a pointer press on an option blurred the list toward an element outside it, so the option
  unmounted before its click landed. `team-navigation-scoping.spec.js` test 3 caught it. Only Tab is
  watched now, so pointer use is untouched. (F2)
- ArrowDown/ArrowUp on the closed picker opens it. (F3)

Tests: `ui/src/components/TopBar.test.jsx` gains one Vitest case covering all three, and it fails
without the fix. `ui/e2e/team-switcher-keyboard.spec.js` covers them against the real demo, plus the
existing keyboard behaviour. Every e2e spec that drives `team-selector` (12 files), plus
`phone.spec.js` and `narrow.spec.js`, passes on Chromium with the fix (62 tests).
