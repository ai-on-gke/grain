# Test run: role-based authorization of writes (2026-10-02 plan §1)

Task: grain/task-1573. Plan: `docs/test-plans/2026-10-02.md` §1. That plan is on `candidate`
(`7e2f08b32`) and is not yet on `main`. Walked 2026-10-02 23:56 UTC to 2026-10-03 UTC.

## Environment

- **Code under test:** `main` at `5a63bb2d1` (Merge PR #2688), the base of branch `grain/task-1573`.
  - `git branch -r --contains 282fe4a02` lists `origin/main` and not `origin/candidate`. The §1 commits are on `main` only, as the plan says.
  - All fifteen §1 commits (`282fe4a02` through `639b3bd79`) are ancestors of `5a63bb2d1`.
  - The fixes below sit on top of it.
- **Machine:** Debian GNU/Linux 12 (bookworm), x86_64, kernel 6.1.0-53-amd64.
- **Toolchain:** Go 1.26.6, Node v20.19.0, Playwright Chromium (`/var/cache/playwright`, chromium-1234).
- **Server:** `go build -o /tmp/grain ./cmd/grain`, then `grain demo -addr 127.0.0.1:0 -open=false` on its embedded throwaway SQLite store.
- **UI:** the production bundle (`cd ui && npm ci && npm run build` into `pkg/ui/static`, embedded in the binary).

## Commands

```
cd ui && npm ci && npm run build
go build -o /tmp/grain ./cmd/grain
TMPDIR=$(mktemp -d) /tmp/grain demo -addr 127.0.0.1:0 -open=false
# API probes: Node scripts doing POST /api/auth/login per persona and replaying the session cookie
# with Origin + Sec-Fetch-Site: same-origin, i.e. a real browser session, not X-Grain-User.
# UI: Playwright against the same server.
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/rbac-writes-walkthrough.spec.js
go test ./pkg/ui/ ./tests/security/ -count=1
make security
```

## Setup (plan step 1)

All setup was done as the deployment's own actor over loopback, with no session.

- **Personas:** registered `viewer`, `member`, `teamadmin`, `repoadmin`, `sysadmin` and `betalead`.
- **Team alpha:** `viewer` has the viewer role, `member` is a member, and `teamadmin` is the admin.
- **Team beta:** `betalead` only.
- **Tasks:** moved demo tasks 1 (proposed), 2 (queued), 6 (awaiting_submit) and 11 (queued) into alpha, and task 9 (deferred) into beta.
- **Repo admin:** `PUT /api/repos/acme/widgets/admins {"admins":["repoadmin"]}`.
- **System admin:** `PUT /api/settings {"adminAccounts":["sysadmin"]}`.
- **Beta's standing work:** a template, a suite, a schedule and a board, created by the local actor. Creating teams alpha and beta also seeded a `Director` console session in each.

Observation: registering a user also creates a personal team named after them, with that user as admin. So `repoadmin` is not literally "in no team". They do belong to their own empty team and to no team that holds work. They read no alpha, beta or default task (404 on each).

## Outcomes

### Step 2: task writes per persona

Probed with real sessions. Each write was followed by a re-read of the task as `sysadmin`.

| Write | viewer | member | teamadmin | repoadmin | beta task 9, any alpha persona |
|---|---|---|---|---|---|
| approve | 403 | 200 | 200 | 404 | 404 `no task 9` |
| run-now | 403 | 200 | 200 | 404 | 404 |
| force-merge | 403 | 200 | 200 | 404 | 404 |
| close | 403 | 200 | 200 | 404 | 404 |
| retarget (`PATCH base`) | 403 | 200 | 200 | 404 | 404 |
| comment | 403 | 200 | 200 | 404 | 404 |
| reorder | 403 | 200 | 200 | 404 | 404 |

- **The 403 message:** `you can read team "alpha"'s tasks but not change them: that needs the member or admin role on it`.
- **No residue:** the task rows were unchanged after every viewer and repoadmin refusal (state, approval, base and comment count).
- **What the UI offers:** a viewer is still shown Approve, Defer, Run now, Close, Edit and the comment box (no role awareness in `Actions.jsx`). Clicking Approve or Comment shows the server's message in the snackbar, which disappears after about 5 s. It is not a blank pane or a spinner.
- **A hidden task:** `/tasks/9` for a non-beta user shows the alert `no task 9` over the task list.
- **Finding F1:** a member's run-now note was attributed to `debian` (the daemon's own actor), not to the member.

### Step 3: cross-team standing work, as `member`

| Attempt | Answer |
|---|---|
| create schedule, template, suite or board in beta | 403 `is not a member of team "beta"` |
| edit beta schedule, template or board; run or delete beta schedule | 404 `no schedule …` |
| move alpha schedule into beta | 403 |
| memory write | 403 (system admin only) |
| **alpha schedule created from beta's template id** | **201**, response titled with beta's template title (F2) |
| **alpha schedule re-pointed (`PATCH templateId`) at beta's template** | **200** (F2) |
| **run that schedule** | **201**, task 13 filed in alpha from beta's template (F2) |
| **alpha suite holding beta's template** | **201** (F2) |
| **alpha task filed with `templateId` = beta's** | **201**, title copied from beta (F2) |
| **schedule draft preview (`/api/schedules/composition`) of beta's template** | **200** with beta's content (F2) |

- **Lists:** member's schedules, templates, suites and boards held no beta objects.
- **Finding F3:** `GET /api/console-sessions` listed every team's sessions (`director@default`, `director-2@alpha`, `director-3@beta`) to viewer and member alike.
- **Fixed result:** after the fix, every F2 row is a 4xx refusal ("unknown template", or 404 for task filing) and nothing is written.

### Step 4: console sessions, as viewer, member and teamadmin

- **With `self-debug`:** creating a session that holds it is 403 for all three ("a system administrator's to set").
- **Plain session:** creating one in alpha is 201 for member and teamadmin, and 403 for viewer.
- **Beta's session:** edit and delete are 403 for all three. Editing alpha's Director while re-sending its privileged capabilities is 403. A title-only rename is 200.
- **Settings:** `GET /api/settings` as sysadmin was byte-identical before and after. The only change to `ConsoleSessions` was the permitted plain sessions.
- **Note:** for beta's session, the refusal to a non-member is a 403 rather than the 404 tasks and standing work give. It confirms the session exists. That is minor, and the list fix (F3) no longer reveals session ids.

### Step 5: credential and state-repo routes, as `member`

Every route answered 403 `user "member" does not have administrator privileges`, both with a valid body and with a malformed `{not json` body. A 403 for the malformed body means the check runs before the body is parsed.

Routes: `PUT/DELETE /api/agent-keys/gemini`, `PUT/DELETE /api/github-tokens/walk`, `PUT /api/github-credential-patterns`, `POST /api/state-repo/{secrets-repo,secrets-key,sync,reset-notice}`, `PUT/DELETE /api/codex-auth`, `PUT /api/agent-backends/codex/codex-auth` and `PUT /api/agent-backends/claude/credential`.

`PUT /api/secrets/walk/k` is 404 "no local secrets directory", which is demo-specific.

### Step 6: body-spoofed `user` / `creator`, as `member` with a real cookie

| Request | Answer |
|---|---|
| `PUT /api/teams/alpha {"user":"sysadmin",…}` | 403 `"member" does not have permission to manipulate team "alpha"` |
| `PUT /api/teams/beta {"user":"sysadmin"}` | 403 |
| `POST /api/teams/copy-credentials {"user":"sysadmin","fromTeam":"default"}` | 403 `"member" is not an admin of source team "default"` |
| `POST /api/secrets/copy {"user":"sysadmin"}` | 403 |
| `POST /api/tasks/2/comments {"user":"sysadmin","author":"sysadmin"}` | 200, attributed to `member` |
| broadcast to alpha `{"user":"sysadmin"}` | 200, comments attributed to `member` |
| **broadcast to `default` (a team member cannot read)** | **200, delivered to 5 tasks including running ones** (F4) |
| **broadcast to alpha as `viewer`** | **200, delivered to 4 tasks** (F4) |
| **`POST /api/tasks {"creator":"sysadmin"}`** | **201, stored `creator: sysadmin`** (F5). The detail pane shows "Creator sysadmin". |

Fixed: the broadcast into another team is a 404, a viewer's broadcast is a 403, and the creator is always the signed-in person.

### Step 7: repo admin

- **Settings writes:** `repoadmin` `PUT /api/repos/acme/widgets/settings` and `PUT …/prompt-extension` are 200, and the change was confirmed as sysadmin.
- **Admin-list writes:** `repoadmin` `PUT …/admins` is 403, and so is `PUT …/settings` with an `admins` field: `does not have permission to change the admins`. The admins list was still `["repoadmin"]`.
- **Member:** `PUT …/settings` and `PUT …/admins` are 403.
- **Release runs:** for `repoadmin`, `POST …/release-runs` with `grain/task-9999` as branch or as ref, with a pinned commit, with `main` plus a pinned commit, and with `main` each answered **404 `this deployment has no grain CI wired`**. The demo has no grain CI, so the reviewed-ref and pinned-SHA refusals in `fb4c3e6bf` cannot be reached here. They remain covered by unit tests only. No 500 was seen.
  - For `member` the same call is 403, even with a malformed body: authorization runs first.
  - For `sysadmin` it is 404 (no CI).

### Step 8: DB grant routes

- **Grant restriction:** `db-read` and `db-write` are restricted to the `default` and `admin` teams in the demo. Attaching either to an alpha task is 400, even for the local actor. So the grant was put on default's running task 3.
- **Answers:**
  - `member` query: 404, since it is not their team.
  - `repoadmin`: 404.
  - `sysadmin` on the live task: 503 `database is not available`. Authorization passed; the demo has no `SourceDB`.
  - After closing task 3: 403 `task "3" is closed: its database access is only for a live run`, for sysadmin and loopback alike.
  - A queued task with a grant: 403 "only for a live run".
  - A same-team member on a live granted task: 403 `database access is for its own run or a system admin`.
- **Observation:** `POST /api/tasks/{id}/capabilities` with an empty body (`{}`) answers 200 and changes nothing. It should arguably be a 400.

### Step 9: Playwright spec

`ui/e2e/rbac-writes-walkthrough.spec.js` encodes steps 1–8 against the real demo. It runs on Chromium with its own demo (distinct `demoArgs`) and passed 4 times in a row locally (about 3 s each).

## Findings

**F1: a run-now note credited the daemon, not the caller (fixed).** `Client.RunNow` wrote its note with `Config.Actor`.

Repro: as `member`, `curl -b <cookie> -H 'Origin: <url>' -H 'Sec-Fetch-Site: same-origin' -X POST <url>/api/tasks/2/run-now`. Then `GET /api/tasks/2` as sysadmin shows the note authored by `debian`.

Fix: `c.actor(ctx)`. Test: `TestRunNowNoteNamesTheCaller`.

**F2: another team's template or suite could be named by id (fixed).** A member could use a template or suite from a team they cannot read in five places:

- a schedule's `templateId` or `suiteId`, on create or on `PATCH`;
- a suite's `templateIds`;
- a task's `templateId`;
- the schedule draft preview;
- the suite draft preview.

The response leaked the template's title and content. A schedule built this way fired tasks into the member's team carrying the other team's template content and grants.

Repro, as `member`:

```
POST /api/schedules {"repo":"acme/widgets","team":"alpha","templateId":"<beta template id>","recurrence":{"kind":"everyNHours","everyNHours":24}}
```

The answer is 201, titled with beta's template title. A `POST /api/schedules/<id>/run` then files the task.

Fix: `Client.readableTemplate` and `readableSuite` in `pkg/ui/team_scope.go` answer nil outside the caller's read scope, and the call sites use them. They are in `scheduleFromRequest`, `UpdateSchedule`, `checkSuiteTemplatesExist`, `fromTemplate` and `DraftSuiteComposition`. grain's own system templates and principal-less callers are unaffected. Test: `TestAnotherTeamsTemplateCannotBeNamed`, which fails on all 7 cases without the fix.

**F3: console sessions were listed across teams (fixed).** `GET /api/console-sessions`, and the list that every session write answers with, returned every team's sessions. Each entry carried its capabilities, repo grants and task id.

Repro: `GET /api/console-sessions` as `member` lists `director-3` (team beta).

Fix: `respondWithConsoleSessions` filters the list by `readScope`. Test: an extension of `TestConsoleSessionWritesAreAuthorized`.

**F4: team broadcast had no team authorization (fixed; most serious).** `POST /api/teams/{id}/broadcast` posted a comment on every active task of any team for any allowed user. That covered a viewer, and someone on no part of the team, whose comments reached running agents' threads (a cross-team prompt-injection path). The response named task ids the caller cannot read.

Repro: as `member`, `POST /api/teams/default/broadcast {"message":"x"}` answers 200 `{"delivered_count":5,"task_ids":["3","4","7","8","12"]}`.

Fix: `BroadcastTeamMessage` asks `authorizeTeamObject` (404 for an unreadable team, 403 for a viewer). Test: `TestBroadcastNeedsAWritingRoleOnTheTeam`.

**F5: the body `creator` overrode the signed-in principal on task filing (fixed).** The team check already used the principal (`authorizeTeamWrite`). But the stored and displayed `creator` was the body's, so a member could file a task shown as created by `sysadmin`.

Repro: as `member`, `POST /api/tasks {"title":"x","team":"alpha","creator":"sysadmin"}` answers 201 with `"creator":"sysadmin"`.

Fix: `handleCreateTask` sets the creator to the verified human when there is one. CLI and agent callers with no human principal may still name one. Tests: `TestASignedInCreatorIsWhoeverIsSignedIn`, and `TestTaskCreationNonMemberCreatorRejected` was updated, since it had asserted the old behaviour where a body creator was checked and stored.

**F6: the UI offers writes the server refuses (not fixed; design).** A viewer sees Approve, Run now, Close, Defer, Edit and the comment box on a task they can only read. The refusal is readable, but the UI should hide or disable these for a viewer. That needs the caller's role per team in the frontend. Filed as a follow-up (proposed by task 1573). Fixed by task 1611: `GET /api/config` now names the teams the caller only views (`readOnlyTeams`). For a task in one of those teams, the task pane, list, board and batch bar leave out the writes and show the server's refusal sentence instead. The walkthrough's step 1 now asserts the viewer is not offered them.

**F7: refusals inside the task dialog are hidden from assistive technology (not fixed; UI).** The snackbar that carries a refusal renders outside the open MUI modal task dialog, which sets `aria-hidden` on everything else. So `getByRole('alert')` cannot find it, and a screen reader does not announce it. It is visible on screen for about 5 s. Filed as a follow-up (proposed by task 1573).

*Update (task 1612):* this was already fixed on `main` by `ab8845762`, which portals `ErrorBanner` to `document.body` so that a modal that is already open does not hide it. The walkthrough spec now asserts these refusals with `getByRole("alert")`. With the pre-`ab8845762` banner that check fails at the first refusal, and with the current banner it passes.

**Observations, not fixed:**

- The viewer's standing-work refusal says `user "viewer" is not a member of team "alpha"`. The viewer is on the team, with a role that cannot write, so "member" is misleading wording.
- Console-session writes to another team's session are 403 rather than 404.
- `POST /api/tasks/{id}/capabilities {}` answers 200 as a no-op.
- Moving a task (as an admin) into a team not authorized for a capability it already holds (task 2 kept `self-debug` in alpha) leaves the grant in place.
- The trusted release-run ref checks cannot be exercised in `grain demo` (404 no CI).

These observations are filed together as one follow-up proposal from task 1573, alongside F6 and F7.

Task 1613 settled the first four:

- A viewer's refusal now names the role it lacks: `user "viewer" can read team "alpha"'s work but cannot edit a schedule in it: that needs the member or admin role on it`. Console sessions say the same. "Not a member" is kept for a caller who really is off the team.
- PUT/DELETE on a console session of a team the caller cannot read now answers 404, before any authorization check, a privileged one included. This matches tasks and standing work.
- A capability toggle with an empty or missing `id` answers 400 `capability id is required`.
- A move detaches every grant the destination team may not hold, whether it was asked for or inherited, and a comment on the task names what it detached (`This task moved to team "alpha", which is not authorized to hold self-debug, so the move detached it.`). Before this, the kept grant was refused at dispatch anyway (`model.ResolveGrants`). Refusing the move was tried and rejected: the New task form files the deployment's default capabilities as explicitly requested, so refusing would block bulk team reassignment of almost every task filed through the UI (`track3-task-intake-inheritance.spec.js` caught this).

The release-run ref checks remain uncovered in `grain demo`. Task 1692 covers them: `grain demo -fake-ci` and `ui/e2e/release-run-ref-checks-walkthrough.spec.js`, reported in `2026-10-03-release-run-ref-checks.md`.

## Fixes made (branch `grain/task-1573`)

- `ca2da2112`: F2 and F1, with `pkg/ui/cross_team_template_test.go`.
- `6ac602aef`: F3, with a `console_sessions_test.go` extension.
- `8c59e034e`: F4 and F5, with tests.
- `f4b233c39`: updated `TestTaskCreationNonMemberCreatorRejected`.
- The e2e spec `ui/e2e/rbac-writes-walkthrough.spec.js`.

- `7e174d471` fixes two e2e specs that depended on which other spec shared their worker's demo. Adding the new spec reshuffled that split, and the first full CI run (`4b7a2a6a2e743e39`) failed `ui e2e` on both. `main` is green on the same suite.
  - `suites_workflows` chose the suite run's repo with `selectOption({ index: 1 })`. When it ran after `non-admin-team-setup-walkthrough`, index 1 was that spec's `acme/innovate-app`, which is not on the target list, so the run was refused with 400. This reproduced locally. The spec now selects `acme/widgets` by name.
  - `team-settings-github-tokens-walkthrough` creates a "Platform Engineering" team, and so do three other specs. Run second on a shared demo, it found another spec's card, which had no Git Name. It now gets a demo of its own.
  - After both fixes, the full Chromium suite passed locally (240/240), and grain CI run `bdc4726b747b708f` passed every job.

These touch `pkg/ui/team_scope.go`, `pkg/ui/console_sessions.go`, `pkg/ui/teams.go` and `pkg/ui/tasks.go`, which are trust-boundary paths, so a security-review hold before auto-merge is expected.

`go test ./pkg/ui/ ./tests/security/` passes. `make security` passes, and no `.grain/baselines/` count changed (no routes were added).

## Stubs used

- **GitHub:** none was called. `grain demo` runs with no GitHub client; its pull-request data is seeded fixture rows, and nothing in §1 reached githubsim or GitHub.
- **Live model sessions:** none. Demo tasks marked running have no agent behind them (`grain demo` dispatches nothing), and no model API was called.
- **Database:** the demo has no `SourceDB`, so authorized DB queries end in 503. That is not a stub, but it limits step 8.
- **Not stubbed:** grain's own APIs, the React UI and the SQLite store are all real.
