# Inbox without read-only teams' tasks, and hidden-tab notifications — 2026-10-07

Plan: `docs/test-plans/2026-10-07.md` §2 (task 2411), run as task 2421.

## What was tested

- **Commit:** `6634d5c81`, the tip of `candidate` when the run started. `grain/task-2421` was cut
  from it. `git branch -r --contains` puts all three commits under test on `origin/candidate`
  (and on `origin/grain-merge-queue/candidate`):
  - `31394cc2c`: the inbox, the rail count and notifications leave out tasks the reader cannot
    change.
  - `c668a16cb`: a row whose reply box is open is kept.
  - `61c31fa31`: a hidden tab with notifications on re-reads `/api/tasks` about every 30 s.
- **Bordering reports, read first:**
  - `2026-10-06-task-intake-inbox-lifecycle.md`: the 10-06 §4 proposer and §6 notifications
    walk, where bug 1 became `31394cc2c`.
  - `2026-10-07-task-intake-inbox-lifecycle.md`: one hidden-tab notification in 28 s.
  - `2026-10-06-daily-exploratory-ui-evening.md`.

  None of them covers the findings below.
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64.
  - Go 1.26.6, Node.js 20.19.0.
  - Playwright 1.62.1 with headless Chromium (`chromium_headless_shell-1234`), with
    `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
  - Viewport 1400×900, and 375×812 for the narrow pass.
- **Servers:** all were the real `grain demo`. Each ran on its own throwaway embedded SQLite store,
  under a scratch `HOME`/`TMPDIR`.
  - The binary was `go build ./cmd/grain` over the production bundle (`cd ui && npm ci && npm run
    build` into `pkg/ui/static`).
  - `127.0.0.1:18090`: API probing of the gate finding.
  - `:18091`: steps 2 and 3.
  - `:18092`: steps 4–7.
  - The e2e specs ran on the fixture's own `grain demo -addr=127.0.0.1:0 -open=false`, rebuilt
    with the fixes.
- **Setup** (`/tmp/w/setup.sh`, run against each server):
  - `pat`, `mel`, `vic` and `ann` were registered through `POST /api/auth/register`.
  - The local trusted admin created team `payments` (`targetRepos: [acme/payments]`).
  - `PUT /api/teams/default` set `pat` proposer, `mel` member, `vic` viewer and `ann` member.
  - `PUT /api/teams/payments` set `ann` viewer.
  - Each user signed in through `POST /api/auth/login` in a browser context of their own.
- **Driver:** ad-hoc Playwright scripts in `/tmp/w` (`s1.mjs`, `s2.mjs`, `s304.mjs`, `s403.mjs`),
  not committed. They logged page errors, console errors and, where noted, every request and
  response.

## Stubs

Nothing of grain's own was mocked: no `page.route`, no React or API fakes. The only stubs were
these two OS boundaries, both installed by `addInitScript` in every context:

- **`window.Notification`:** a recorder class. Its `permission` is `granted` and
  `requestPermission()` resolves to `granted`. Each constructed notification is kept with its
  title, body, tag and time in `window.__notes`. Clicking a notification was done by calling
  grain's own `onclick` on the recorded instance.
- **Page visibility:** `Document.prototype.visibilityState` and `hidden` are getters over a
  variable, and `document.hasFocus()` follows it. `window.__setVis(v)` sets the variable and
  dispatches `visibilitychange`.

The new e2e spec also uses Playwright's own clock (`page.clock`) to pass minutes.

## Steps and results

### 1. Users and contexts — pass

Each of `pat`, `mel`, `vic` and `ann` signed in through a separate browser context. `/api/config`
gave:

| User | `readOnlyTeams` |
| --- | --- |
| `pat` | `["default"]` (`proposerTeams: ["default"]`) |
| `mel` | none |
| `ann` | `["payments"]` |

### 2. Filing while `mel`'s tab is hidden — pass

- `mel` opened `/inbox`. With permission granted, the header read **Turn off notifications**.
  Both `mel`'s and `pat`'s tabs were then hidden.
- **Through New task:** `pat` filed "Walk: filed through New task" on `acme/widgets`. The form
  answered `201`, and #13 was `proposed`.
  - `mel` got exactly **one** notification **28.2 s** later: "13 was proposed and needs
    approval", tag `task:13:proposed`.
  - `pat` got **none**. `pat`'s tab was hidden too, so nothing but the inbox filter kept it quiet.
  - Clicking it opened `/tasks/13` with **Approve** visible.
- **Through the API:** `pat`'s session cookie was used for `POST /api/tasks`. The answer was
  `201`, and #14 was `proposed`.
  - `mel` got exactly one notification after **29.2 s**, and `pat` got none.
  - The click opened `/tasks/14` with Approve.

### 3. Ten minutes of a hidden tab — finding 2 (fixed)

`mel`'s tab stayed hidden on `/inbox` for 10 minutes with notifications on. #15 was filed halfway.

- **Requests:** 30 in total.
  - 20 were `GET /api/tasks`, at intervals of 28.9 s, then exactly 30.0 s nineteen times.
  - **10 were `GET /api/base-branch-gates`, once a minute.** The plan expected only
    `/api/tasks`.
  - Nothing else was read: no `/api/config`, sessions or CI runs.
- **Statuses** (`s304.mjs`, a separate ~190 s hidden run with one arrival): `/api/tasks` returned
  304 five times and 200 once (the arrival). `/api/base-branch-gates` returned 200 three times;
  it is not conditional.
- **Arrival:** #15 raised exactly one notification while hidden.
- **Showing the tab:** the full poll resumed within **0.01 s**: `/api/tasks`,
  `/api/console-sessions`, `/api/ci/runs` and `/api/config`. **No** notification was raised again.
- **Notifications off:** **Turn off notifications** became **Turn on notifications**. The tab was
  hidden for 150 s and a task was filed meanwhile.
  - No notification was raised.
  - **But two requests were made: `/api/base-branch-gates` ×2.** 61c31fa31 says that with
    notifications off "a hidden tab polls nothing, as before". The gate poll's own effect never
    looked at visibility. This is finding 2.

### 4. Two hidden tabs of `mel` — pass, as designed

One filing (#13 on `:18092`) raised **two** notifications, one per tab, 19 ms apart. They carried
the **same tag**, `task:13:proposed`.

`announce` in `inboxNotifications.js` sets the tag to the entry's key on purpose: "two grain tabs
open on the same deployment replace each other's notification rather than stacking two". A real
browser therefore shows one. Intended, not filed.

### 5. Live role changes, no reload — pass

- **Before:** `pat`'s inbox was empty, with a rail **Inbox** and no count. `mel`'s inbox had 5
  rows, and the rail read 5.
- **`pat` promoted to member:** within one poll, without a reload, `pat`'s page showed rows
  #4, #6, #7, #1 and #13, with two **Approve** buttons. The rail read 5, matching the page.
- **`mel` demoted to viewer:** before the demotion, `mel` opened **Reply** on #4 and typed "Draft
  typed before demotion". After it:
  - only #4's row stayed, with the box and its text intact;
  - every other `default` row left, and the rail dropped to no count.
  - **Observation, not filed:** while the reply box stays open, the page's own count includes the
    kept row (`waiting.length` in `InboxPage`), so it reads 1 while the rail (`inboxTasks`) reads
    0. `c668a16cb` says nothing about the count. The mismatch lasts only while a reader keeps a
    refused draft open, and it ends on Cancel, so the agreement the plan asks for holds once the
    row leaves.
  - **Send** gave `POST /api/tasks/4/comments` → **403**, shown as an alert: 'you can read team
    "default"'s tasks but not change them: that needs the member or admin role on it'. The text
    stayed.
  - **Cancel** removed the row, leaving 0 rows and no count.
- **Notifications:** none were raised by the demotion. None were raised when `mel` was promoted
  back while hidden either: rows that come back are already in the page's seen set, so they are
  not re-announced.

### 6. `ann` on "All teams", and a `payments` gate — finding 1 (fixed in the browser)

- The team switcher read "All teams". A `payments` task (#14, `proposed`, team `payments`) did
  **not** reach `ann`'s inbox. The rail read 5, all `default` work. Pass.
- **Gate:** the admin made `acme/payments`' gate fail by setting
  `PUT /api/repos/acme/payments/watch-default-branch {"watchDefaultBranch": true}`. With no grain
  CI, the finding is `grain-ci-off`.
  - `ann`'s inbox gained a **"Look at a base branch nothing is judging"** row for `acme/payments`,
    and the rail went from 5 to **6**.
  - `ann` cannot act on it: `PUT /api/repos/acme/payments/ci-capabilities` answers **403** ('user
    "ann" does not have permission to modify repository acme/payments').
- `mel`, who has **no role at all** on `payments`, saw the same row. `pat` and `vic` were handed
  it by `GET /api/base-branch-gates` too. This wider part is the follow-up below.

### 7. Keyboard-only and 375 px as `pat` — pass

- **Keyboard:** from `/`, Tab reaches the rail's **Inbox** item after 31 stops, following
  "+ New task", **Create options** and **Customize pinned items**. Enter opens `/inbox`, which
  shows "Nothing is waiting on you. Every task is either running, queued or done.": an empty
  state, not an error.
- **375×812:** `/inbox` shows the same empty state with no horizontal overflow
  (`scrollWidth - clientWidth = 0`) and no alert.

### Console

On every page load, for every non-admin, the only console errors came from `GET /api/state-repo`
→ 403. That is already recorded in `2026-10-03-responsive-navigation-live-sessions.md` and
`2026-10-04-deployment-operations.md`, so it was not re-filed. There were no page errors.

## Findings

### 1. A read-only team's base-branch gate reached its viewer's inbox (fixed here)

**Reproduction:** `grain demo`.

1. Run the setup above (`ann`: member of `default`, viewer of `payments`, which targets
   `acme/payments`).
2. As the admin, `PUT /api/repos/acme/payments/watch-default-branch {"watchDefaultBranch":true}`.
3. Sign in as `ann` and open `/inbox`. There is an `acme/payments` row under "Look at a base branch
   nothing is judging", and the rail counts it. With notifications on, a hidden tab announces
   "acme/payments is not being tested".
4. `PUT /api/repos/acme/payments/ci-capabilities` as `ann` gives 403.

**Cause:** `31394cc2c` filtered tasks through `taskWriteRefusal`. `inboxGates` had no reader at
all.

**Fix:** `gateWriteRefusal(config, gate)` in `ui/src/taskWrites.js`.
- It reads a gate's owning teams from `config.teams[].targetRepos`, as `model.RepoTeams` reads
  ownership: case-insensitively, with `*` inside a segment, and without bare `*` / `*/*`.
- It refuses the gate when every owner is in `readOnlyTeams`.
- `inboxGates(gates, config)` drops refused gates. `inboxCount`, `InboxPage`, the rail
  (`Sidebar`) and `useInboxNotifications` pass `config`.
- An admin's config has no `readOnlyTeams`, so nothing changes for admins.

**Not covered by the fix:** a repository owned by a team the reader is not on at all, as in
`mel`'s case. `config.teams` lists only the reader's teams, so the browser cannot tell who owns
it. A follow-up was proposed: scope `GET /api/base-branch-gates` on the server. It depends on
this task and is not auto-merge, because it is a judgement call against grain/task-470's "the
inbox is where somebody has to deal with this".

### 2. A hidden tab with notifications off still read `/api/base-branch-gates` every minute (fixed here)

**Reproduction:**

1. Open `/inbox` with notifications permitted.
2. Click **Turn off notifications**.
3. Set `visibilityState` to `hidden` and log requests for 150 s.
4. Two `GET /api/base-branch-gates` are made, one a minute.

`App.test.jsx`'s own hidden-poll case said "the base-branch gates keep their own minute's clock
either way".

**Fix:** in `App.jsx`, the gate effect's `load` returns early when the tab is hidden and
`notificationsEnabled()` is false. A `visibilitychange` to `visible` reads the gates at once.

With notifications on, a hidden tab still reads the gates once a minute, beside `/api/tasks` every
30 s. A gate finding is something those notifications exist to deliver, so that was kept, and the
comment says why.

## Regression tests

Each new or changed test below was run with the fix stashed and failed.

- Vitest:
  - `taskWrites.test.js` "gateWriteRefusal" (4 cases).
  - `state.test.js` "leaves out a gate on a repository the reader only views". Failed without the
    fix.
  - `inboxNotifications.test.js` "does not announce a gate on a repository its reader only views".
    Failed without the fix.
  - `App.test.jsx` "keeps reading the task list in a hidden tab only while notifications are on".
    It now also asserts no request at all over three gate intervals with notifications off, and a
    gate read on show. Failed without the fix.
- Playwright:
  - `e2e/inbox-read-only-teams.spec.js` "a viewer's inbox leaves out a gate on the read-only team's
    repository". Failed without the fix: `getByText('Look at a base branch nothing is judging')`
    resolved to 1 element.
  - New `e2e/inbox-hidden-tab.spec.js`, which uses Playwright's clock. Failed without the fix:
    received three `/api/base-branch-gates` reads where none were expected.
- `npm run format` was run.

## CI verdict

- `frontend` (selective) passed on `7081dee5d`.
- **Full suite on `9b3aeca29` and `1ddce24e7`:** every job passed except `ui e2e`, which failed
  once on `[webkit] every list opens a row from the keyboard and Back returns focus to it`
  (`e2e/candidate-keyboard-walkthrough.spec.js:56`).
  - WebKit reported "Fetch API cannot load …/api/tasks (…/api/ci/runs, …/api/console-sessions)
    due to access control checks". That is WebKit cancelling the shell's poll on navigation.
  - The same error failed `main`'s run `ef7ab81fddf34a58` at `a1cd7b3` on another test in the same
    file.
  - Locally the spec passed 3 of 3 times on WebKit with this branch's build.
  - An uncached `retrigger_ci` of `tests.yml` (`c23cded67764be50`) passed `ui e2e` (430 passed,
    1 skipped). Recorded as a recurrence of a WebKit flake, not chased here.
- The final full-suite verdict on this branch's head is in the task's closing note.
