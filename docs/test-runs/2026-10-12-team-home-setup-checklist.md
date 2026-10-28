# Team home page and setup checklist walkthrough

- **Plan:** `docs/test-plans/2026-10-12.md` §9 (read from `origin/candidate`).
  Feature `1f201114a` (origin task 3024). It is on `main` and not on
  `candidate` yet. Run by task 3292.
- **Date:** 2026-10-13
- **Commit tested:** `f3dfcaa5c` (tip of `main`, checked out as
  `grain/eu/task-3292`). The fix below was then re-verified on a binary rebuilt
  from it.
- **Environment:** Debian GNU/Linux 12 (bookworm), Go 1.26.6, Node v20.19.0,
  `@playwright/test` 1.62.1. The browser was the Playwright Chromium headless
  shell (v1234). `npx playwright install chromium` fails in this sandbox with
  `SELF_SIGNED_CERT_IN_CHAIN` unless `NODE_EXTRA_CA_CERTS` points at
  `/usr/local/share/ca-certificates/grain-ca.crt`.
- **Server:** `grain demo -addr 127.0.0.1:0 -open=false`, built from this tree
  (`cd ui && npm ci && npm run build`, then `go build -tags grain_e2e
  ./cmd/grain`), on the throwaway embedded SQLite store it creates under
  `TMPDIR`.
  - For manual exploration, one server was started by hand and polled at
    `GET /api/sandboxes` until it answered (`{"enabled":false}`, 200).
  - For the spec, each Playwright worker starts its own server through
    `e2e/fixtures.js`. That fixture waits for the server's "at http://…" line,
    then checks `GET /api/config`.
  - Demo args: `-default-target-repo acme/widgets`.
- **Browser driving:** `ui/e2e/team-home-setup-checklist-walkthrough.spec.js`,
  committed with this report. It runs against the built frontend. The system
  admin's requests carry `X-Grain-Admin-Mode: on` (the suite default). Members,
  viewers and the other team's admin get contexts of their own with
  `extraHTTPHeaders: {}`, signed in through `POST /api/auth/login`.
- **Model/tooling:** nothing Gemini-backed was used. Model connections were
  saved, not called.

## Stubs

No external boundary was reached, so nothing was stubbed: no GitHub, no model
provider, no live session.

- The git connection and the legacy token are fake strings
  (`ghp_walkthrough_…`). They were saved through the real UI. Nothing tested
  them against GitHub.
- The model connection (Antigravity, Gemini API key) was saved with no key.
  Nothing dispatched on it.
- grain's own API was used directly only for setup and read-back, never mocked:
  - Setup: `POST /api/auth/register`, `POST /api/teams` for the role and
    dialog teams, and `POST /api/templates` for the suite's templates.
  - Read-back: `GET /api/teams/:id`, `/api/tasks`, `/api/tasks/:id`,
    `/api/schedules`, `/api/templates` and `/api/suites`.
  - Step 6 also closes one task with `POST /api/tasks/:id/close` (see that
    step).

## Steps and results

| # | Step | Result |
|---|------|--------|
| 1 | As a system admin, create an empty team through **+ New ▸ New team** and open `/t/<team>`. | **Pass.** The checklist reads "Set up Setup Walk: 4 steps left", with all four items `data-done="false"`. The top-bar notice says "Team Setup Walk: 4 setup steps left — its tasks wait until it has a git host and a model connection." The home page hides the notice by design (`App.jsx`: it shows the checklist the notice links to), so the notice was read on the team's task list, opened from the Work card. |
| 2 | Follow each checklist link and complete the step: add a member (Members tab), add a git connection (Git tab, **Add connection**), add a model connection (Connections tab), add a repository (Repos page). | **Pass.** Each button opens the right Team settings tab, or the Repos page for a repository. After each save and close, the item ticks off and the checklist and notice count drop (4 → 3 → 2 → 1 → 0). A `window` marker set before the first step survived to the end, so nothing reloaded. At zero, both the checklist and the notice are gone. |
| 3 | Repeat with a legacy `gitCredential`: the Git tab's **GitHub token** editor (`PUT /api/teams/:id/github-credential`) instead of a connection. | **Pass.** "Connect a git host" ticks off. `GET /api/teams/:id` shows `hasGitCredential: true` and no `gitConnections`. The remaining steps tick off the same way, and at zero the notice goes. |
| 4 | View the home page as a member, as a viewer, and as another team's admin. | **Pass.** Member and viewer see the checklist with "A team admin finishes these.". It has no buttons. There is no Edit button anywhere on the page and no "Models" button, and Repositories says "Open" rather than "Manage". No notice appears on the home page or on the task list. No request returned 403 during either visit. The task list's empty state explains teams ("Teams own their repositories, credentials and model connections. You're working in Roles Walk."). Another team's admin following `/t/<team>` gets 'no team "…" you are a member of'. They land on their own team's home, and nothing mentions the other team. Their own unfinished team's notice ("Team Elsewhere: 3 setup steps left") still shows on its task list. |
| 5 | In each creation dialog, opened under team Alpha, change the team picker to Beta, create, and check the stored item. The dialogs are New task, New board, New session, New schedule, New template and New suite. | **Pass.** In all six, "In team Alpha Walk" becomes "In team Beta Walk" when the picker changes. Each item is stored with `team: beta-…`: task and board via `GET /api/tasks/:id`, the session from the `POST /api/console-sessions` answer, and schedule, template and suite via their lists. New task's repo picker follows the team too: Beta's repos replace Alpha's. New board's task targets `grain/config-<beta>`. The suite form's template list does **not** follow the picker (F2). |
| 6 | Compare the home page's Running/Queued counts with `/t/<team>/tasks?state=running` and `?state=queued`, then file a task and close a running one. | **Failed, then fixed (F1).** Before the fix, the default team's home said **Queued (3)** while the queued list showed **2** rows. After the fix the counts match the list (Running 2, Queued 2). Filing a task through New task (approved by default, so queued) moved Queued to 3 on the home page without a reload, and the list agreed. Closing a running task moved Running to 1 without a reload, and the list agreed. `grain demo` has no dispatcher (no sandboxes), so tasks cannot really start or finish. State changes came from filing a task through the UI and closing one through the API. |
| 7 | Dismiss the switcher hint with **Got it**, then reload. | **Pass.** The hint is gone after **Got it**. It stays gone after a reload and on `/t/default` (`localStorage["grain.teamsHintSeen"] = "1"`). |

## Findings

### F1: the home page counted a standing session's own task as queued (fixed)

- **Repro:** run `grain demo` and open `/t/default`. The Work card says
  "Queued (3)". Open `/t/default/tasks?state=queued` (or click **All tasks**,
  then Queued in the rail). The list shows 2 rows, and the rail says
  "Queued 2".
- **Cause:** the demo seeds task 12, "Director Session", a standing session's
  own task (`consoleSession` set), in state `queued`. The task list leaves
  such tasks out (`taskFilters.js`'s `hidesStandingSessions`), and so do the
  rail's counts (`Sidebar.jsx`), because the session lives under Sessions.
  `TeamHomePage.jsx` counted every task of the team.
- **Fix:** `TeamHomePage.jsx` leaves `consoleSession` tasks out of its Work
  card.
- **Regression tests:** a Vitest case in `TeamHomePage.test.jsx` ("leaves
  standing sessions' own tasks out of the counts"), which fails without the
  fix. The e2e step 6 case also checks that the demo still seeds such a task,
  so the comparison keeps exercising it.

### F2: the suite form offers the page's team's templates, whichever team its picker names (filed)

- **Repro:** create teams Alpha and Beta, each with a template. On
  `/t/alpha/suites`, open **+ New suite** and switch "Target team" to Beta.
  "In team Beta Walk" follows, but **Templates** still lists only Alpha's
  template. Saving creates a Beta suite that runs an Alpha template, and the
  server accepts it.
- **Cause:** `App.jsx` hands `SuitesList`/`SuiteOverlay` the `visibleTemplates`
  of the selected team, and the overlay's own `team` never narrows them.
- **Status:** not fixed here. Whether a suite may run another team's templates
  is a design and RBAC question: the server allows it today. Filed as a
  follow-up task against `main`. The e2e "New suite" case picks the Alpha
  template on purpose, with a comment pointing here.
- **Origin:** the picker predates `1f201114a`. That commit only added the "In
  team X" line above it.

### Notes (no change made)

- The switcher hint renders under the first-run "Create account" dialog for a
  browser with no `grain.username`. The dialog is modal, so the hint cannot
  be dismissed until the dialog is dealt with. After that it behaves as above.
- The plan's step 1 expects the notice on the home page. By design it is
  hidden there and shown on every other page, as recorded above.
- The plan says "as tasks dispatch and finish in demo". `grain demo` runs no
  dispatcher (`/api/sandboxes` answers `{"enabled":false}`), so step 6 moved
  tasks by filing and closing them instead.

## Fixes in this change

- `ui/src/components/TeamHomePage.jsx`: exclude standing sessions' own tasks
  from Running/Queued (F1).
- `ui/src/components/TeamHomePage.test.jsx`: regression case for F1.
- `ui/e2e/team-home-setup-checklist-walkthrough.spec.js`: this walkthrough,
  11 cases, all passing locally in 38 s.
