# System section rename and off-team boards walkthrough

Test plan: `docs/test-plans/2026-10-12.md` §4. Commits under test: `a0536b15d`
(Admin section renamed to System), `40f57abf5` (a team's board opened under
another team) and `4fc2ac115` (the run dialog's suites ordered like the suites
page). All three are reachable from `candidate`.

- **Date:** 2026-10-12
- **Commit tested:** `c8a3f5e0c2f58abf31ebd8af1bf855a22842a897`, the tip of
  `candidate`, checked out as `grain/eu/task-3289`. The fixes below were
  re-verified on a build of this branch.
- **Environment:** Debian GNU/Linux 12 (bookworm), Go 1.26.6, Node v20.19.0,
  `@playwright/test` 1.62.1, Playwright Chromium headless shell v1234
  (installed with `NODE_EXTRA_CA_CERTS` pointing at grain's package-proxy CA).
- **Server:** the UI was built with `cd ui && npm ci && npm run build`, then
  `go build ./cmd/grain`. The server ran as
  `grain demo -addr 127.0.0.1:18080 -open=false` on its own throwaway SQLite
  store. A fixed port was used so the ad-hoc scripts could share one server;
  the committed specs use the suite's own per-worker `-addr 127.0.0.1:0`
  demo. Readiness was checked by polling `GET /api/sandboxes`.
- **Browser driving:** ad-hoc Playwright scripts (not committed) for the
  sweep, plus the committed specs listed under Fixes. Each role was a real
  signed-in user (`POST /api/auth/login`, then
  `localStorage["grain.username"]`). The system admin was run twice: with
  `X-Grain-Admin-Mode: on`, and with no extra headers. Every other role sent
  no extra headers (`extraHTTPHeaders: {}`).
- **Setup:** users `sysadmin` (the only entry in `adminAccounts`, and removed
  from the default team), `plain` (a member of the default team), `both` (on
  teams A and B) and `bonly` (on team B only). Team A (`teama`) has three
  tasks and a list board `board-5`. Team B (`teamb`) has two tasks and a
  list board `board-6`.

## Stubs

1. **Board rows were posted to `POST /api/boards`.** In the UI, "New board"
   files a task whose pull request against the team's config repository adds
   the board. The demo cannot merge that pull request.
   `e2e/team-board-off-team.spec.js` and the 2026-10-12 teams walkthrough do
   the same.
2. **Setup went to grain's own API** over loopback, as the deployment's own
   actor: `/api/auth/register`, `/api/teams`, `/api/settings`
   (`adminAccounts`), `/api/tasks`, `/api/templates` and `/api/suites`. Task
   state was read back with `GET /api/tasks/:id`, and team-B-only access was
   checked with `GET /api/boards`, `/api/tasks` and `/api/tasks/13`.

Nothing was mocked. There was no GitHub and no live agent session; the demo's
own fake state stood in for both.

## Steps and results

### 1. Sweep for user-visible "Admin"

The sweep covered three roles: system admin with Admin mode on, system admin
with Admin mode off, and a plain member. Each was run at 1280×900 and at
375×812 (phone), for 28 routes:

- Inbox, tasks (open and closed), a task's detail and every tab of it.
- Repos and a repo page with its tabs, CI, schedules, templates, suites,
  boards, sessions, sandboxes, caches, and Metrics with its tabs.
- Settings, and Team Settings with every tab.
- `/new`, `/admin` with every System tab, `/director`, and an unknown path.

On every page it also opened:

- The `?` key.
- The team picker.
- The Customize-pinned overlay, with its icon picker opened.
- The phone navigation drawer (at 375×812).

On each page and dialog the sweep recorded every line of `innerText`, and
every `title`, `aria-label`, `placeholder` and `alt`, that matched `/admin/i`.

Toasts and confirm prompts cannot all be triggered from a sweep. The UI
source (`ui/src`, excluding tests and comments) was also grepped for `Admin`
as a word, and for "admin pane/section/settings" or "Admin →" in strings.

Hits that are not "Admin mode" or a role or team name:

| # | Where | Text | Who sees it |
|---|-------|------|-------------|
| 1 | System → General & Deployment, the System Administrators note | "Users listed here have system administrator permissions and can access **Administrator settings**." | system admin (Admin mode on), desktop and phone |
| 2 | Customize pinned items → Icon picker (both the per-item picker and "Icon (optional)") | "**Admin** (Core case)", listed beside "System (Core case)": the same figure under both names | every role, desktop |

Hits that were correctly left alone:

- Role names: "Admin" in the role picker and its explanation, "Admins:",
  "member or admin", "team admin", "only this team's admins",
  "deployment admins", "System Admin" (the user chip),
  "system administrators", "add system admin", "Admin-only: held by …" (a
  capability held by the admin team list), and the `title` "The last admin
  of a team cannot be removed".
- "Admin mode" and its tooltip "You are a system admin. Turn on to use your
  admin rights."
- **The demo's default team is named `admin`.** That accounts for most raw
  hits: "admin (default)", "In team admin", "You're working in admin", and
  "default capabilities for admin".
- Personal teams named after the user (`sysadmin`), and team ids in error
  text.

No "Admin" label was found in:

- The sidebar entry (it reads "System").
- The tab strip (it is labelled "System tabs").
- The phone drawer.
- Empty states.
- The 404 fallback.

Pressing `?` on each page added no "Admin" text. The sweep did not check
whether that key opens a help dialog on this build. The UI source has no
links to repository docs, only code comments that name them.

### 2. `/admin` and `/system` deep links

| Path | sysadmin, Admin mode on | sysadmin, Admin mode off | plain member |
|------|-------------------------|--------------------------|--------------|
| `/admin` | `/admin`, General & Deployment | `/admin`, Sandboxes (tabs: Sandboxes, Caches) | same as Admin mode off |
| `/admin/engine` | `/admin/engine`, Engine & Concurrency | URL stays `/admin/engine`, shows Sandboxes | same |
| `/admin/caches` | Caches | Caches | Caches |
| `/admin/bogus` | General | Sandboxes | Sandboxes |
| `/system` | **rewritten to `/admin/logs`, Logs tab** ❌ | rewritten to `/admin/logs`, shows Sandboxes | same |
| `/system/engine` | `/admin/engine`, Engine & Concurrency | `/admin/engine`, Sandboxes | same |
| `/system/caches` | `/admin/caches`, Caches | `/admin/caches`, Caches | same |

Every path opens the pane headed "System". Gating holds: a non-admin is shown
only Sandboxes and Caches, whatever tab the link names.

### 3. A's board opened with B picked, by a user on both teams

- The link `/t/teamb/boards/board-5` lands on `/t/teama/boards/board-5`. The
  team picker moves to Team A, and the rows are A's three tasks; no B task is
  shown.
- No "Team A's board" chip appears, and no off-team rendering is involved:
  the page has moved to A. See finding 3.
- Ticking two of A's rows shows "2 selected". Defer deferred exactly tasks 13
  and 14 (A). Task 15 (A, unticked) and tasks 16 and 17 (B) were unchanged. ✅
- Back after the redirect returns to B's task list, not to a mismatched entry:
  the redirect replaces the history entry. ✅

### 4. A's board opened by a user on B only

- `/t/teamb/boards/board-5` and `/t/teama/boards/board-5` both land on
  `/t/teamb/boards`. The page shows B's board list ("teamb board") and the
  note `No board "board-5"; no team "teama" you are a member of`. That note
  only echoes the ids in the URL. ✅
- No A task title or A board name appears anywhere on the page. Rail counts
  are B's (tasks 2, boards 1). "Select all" on B's list shows "2 selected". ✅
- The API agrees: `GET /api/boards` returns only `board-6`, `GET /api/tasks`
  returns only B's two tasks, and `GET /api/tasks/13` returns 404. ✅

### 5. Run dialog suite order (`4fc2ac115`)

A new spec creates "Aardvark …" (one template) and then "Zebra …" (two
templates), so the API's newest-first order is the reverse of name order. It
then checks that the bare "+ Run" dialog preselects the page's first row and
lists the suites in the page's own order. It checks this twice: under the
default (name) order, and again after choosing "Most templates". It passes on
`candidate`. ✅

## Findings

1. **Bare `/system` opened the Logs tab, and `/admin` opens General**
   (deterministic). Open `/system` as anyone: the URL becomes `/admin/logs`.
   For a system admin with Admin mode on, the Logs tab is selected.

   The cause is `App.jsx`'s initial `adminTab`, which defaulted `/system` and
   `/system/...` to `"logs"`. That is left over from when `/system` was the
   Debug pane (`5fff30ccf`). Back and Forward onto `/system` already chose
   General.

   **Fixed.**
2. **Two stale "Admin" labels** (deterministic): sweep hits 1 and 2 above.
   **Fixed.**
3. **The plan's description of `40f57abf5` no longer matches `candidate`**
   (not a user-facing bug). The plan says a board opened under another team
   draws its own team's tasks with a chip naming the team.

   On `candidate`, the back-merge `172fa2f17` deliberately kept main's form of
   that fix instead: a link to a board moves the page to the board's own team
   (grain/task-3088). `openBoardOffTeam` and the `otherTeamName` chip are not
   in the tree, and only `e2e/team-board-off-team.spec.js` remains. What users
   get is still correct (steps 3 and 4).

   One residual remains. The popstate handler sets the team from the URL and
   opens the board without following it to its own team. A history entry
   naming `/t/teamb/boards/board-5` therefore draws "teama board" over B's
   tasks, with no chip. Reproduce it by opening `/t/teamb/tasks` as `both` and
   running `history.pushState({}, "", "/t/teamb/boards/board-5");
   dispatchEvent(new PopStateEvent("popstate"))`.

   Nothing in the app writes such an entry: the redirect uses
   `replaceState`, and switching team closes the board. So it is reachable
   only from a script. It is recorded here and not fixed.
4. **Non-admins keep a tab path they cannot see** (cosmetic, not fixed).
   `/admin/engine`, opened by a non-admin, shows Sandboxes but leaves
   `/admin/engine` in the address bar. The same happens for `/admin/bogus`.
   A copied link still opens correctly for whoever reads it.

## Fixes in this change

- `ui/src/App.jsx`: a bare `/system` opens on General, like `/admin`.
- `ui/src/components/AdminOverlay.jsx`: the note now reads "can open every
  System tab".
- `ui/src/pinnedNav.js` and `ui/src/components/CustomizePinnedOverlay.jsx`:
  the duplicate "Admin (Core case)" icon entry is gone. A shortcut already
  saved with the `admin` icon still draws the same figure, and the picker
  shows it as "System" (`pickerIconId`).
- Regression tests:
  - `ui/src/pinnedNav.test.js` (Vitest): the picker offers no "Admin" label,
    and `pickerIconId` maps `admin` to `system`.
  - `ui/e2e/system-rename-walkthrough.spec.js`: `/admin`, `/system` and
    `/system/engine` landings for a system admin, the System Administrators
    note, and the icon picker. It also checks `/admin`, `/system`,
    `/admin/engine`, `/system/engine` and `/system/caches` for a member
    without Admin mode. The three admin tests fail on unfixed `candidate`.
  - `ui/e2e/team-board-cross-team-access.spec.js`: steps 3 and 4, as real
    signed-in users.
  - `ui/e2e/suite-run-dialog-order.spec.js`: step 5.

## Notes

- The learned note that sandboxes have no Gemini key is out of date for this
  run: `/home/debian/.gemini-api-key` was present. It was not used.
