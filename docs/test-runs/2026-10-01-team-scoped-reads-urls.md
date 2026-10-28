# Test run: team-scoped reads and team-scoped URLs with two browser users

- **Plan:** `docs/test-plans/2026-10-01.md` §4 (read from `candidate` at
  `5b367927b`; the plan has not reached `main`, and branch
  `grain/task-1353` no longer exists on the remote).
- **Task:** grain/task-1413
- **Date:** 2026-10-01
- **Tested commit:** `main` at `35aa96215` (contains `87e82c498`,
  `ce53270d0`, `e4b4f755c` and `19076a98e`). Fixes are on
  `grain/task-1413` on top of it.

## Environment

- Debian sandbox, Go 1.26.6, Node 20.19.0, Playwright Chromium 1234 from
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- UI built with `cd ui && npm ci && npm run build`, so the server serves
  the production bundle embedded from `pkg/ui/static`.
- `go build -o /tmp/grain ./cmd/grain`, then
  `grain demo -addr 127.0.0.1:0 -open=false` on its throwaway SQLite
  store. Password auth through `/api/auth/register` and
  `/api/auth/login` (session cookie).
- **No stubs.** Nothing in grain's API or UI was mocked. Setup went
  through grain's own API, as the deployment's actor over loopback:
  - registered `admin`, `alice`, `bob` (and, in the manual run, `carol`).
  - `PUT /api/settings {"adminAccounts":["admin"]}`. The demo seeds
    `alice` as a system admin, and she has to be a plain member here.
  - `POST /api/teams` created `red` (alice) and `blue` (bob).
    `PUT /api/teams/red {"targetRepos":["acme/widgets"]}`.
  - `POST /api/tasks/3/move {"team":"blue"}` (demo task 3 has attempt 1
    with a transcript) and `POST /api/tasks/8/move {"team":"red"}`.
  - `POST /api/tasks/{3,8}/comments` with one text attachment each.
- **Caveats.**
  - `grain demo` has no secrets directory (`ui.Config.Secrets` is nil),
    so `/api/secrets` answers `{"enabled":false}` there. The secrets
    steps were run against a real `secrets.Store` in a Go test instead
    (see step 4).
  - The UI does not use SSE. It polls `/api/tasks` with `If-None-Match`
    (`ui/src/api.js`, `poll.js`). The live update step used that real
    poll. The team event stream `/api/teams/{id}/events` was exercised
    with curl.
  - Requests that carry no principal, from loopback, run as the
    deployment's actor and are not team-scoped. This is by design
    (`e4b4f755c`), and it is how setup ran.

The walkthrough is now
`ui/e2e/team-scoped-reads-walkthrough.spec.js`. It runs on a demo of its
own (`test.use({ demoArgs: [...] })`), because it changes the demo's
admin list and team membership.

## Steps, expected vs observed (on `main`, before fixes)

| # | Step | Expected | Observed on `main` | After fix |
|---|------|----------|--------------------|-----------|
| 1 | Boards in three contexts | alice sees red only, bob sees blue only, admin sees all | As expected: alice `[8]`, bob `[3]`, admin all 12 | same |
| 2a | alice opens `/tasks/3` (bob's) | not-found UI or 404 | URL is rewritten to `/` and nothing of task 3 is shown | same |
| 2b | alice: `GET /api/tasks/3`, `/prompt`, `/messages`, `/attachments/1`, `/attempts/1/transcript` | 404 | 404 `no task 3` for every one | same |
| 2c | alice: `GET /api/tasks?team=blue` | 403 | **200, with alice's own list** (`?team=` was ignored) | 403 |
| 2d | alice: `GET /api/teams/blue/events` | 404/403 | 404 `no team blue` | same |
| 2e | alice: `GET /api/tasks/states` | own team's tasks only | **every team's task IDs and states** | red only |
| 2f | alice: `POST /api/tasks/3/comments`, `/close`, `PATCH /api/tasks/3`, `/move` | refused | **200 for all of them.** Bob's task was closed, commented on and renamed, and each response carried bob's whole task | 404, and the task is untouched |
| 2g | alice: `POST /api/tasks/reorder {"ids":["3"],"afterId":"8"}` | refused, own list only | **200, answered with all 13 tasks of every team** | 404 when naming 3; own list only when reordering 8 |
| 3a | Interleaved `If-None-Match` polls (alice/bob, each with the other's ETag) | never a cross-team 304 or body | As expected: 200 with own body against the other's ETag, 304 only against own | same |
| 3b | bob files a blue task, then both poll | alice 304 against her own ETag, bob 200 with the new list | As expected | same |
| 3c | alice joins, then leaves, blue | stale ETag gets 200 with the new scope | As expected (spec) | same |
| 3d | Live board | bob's tab shows his new task, alice's never does | As expected | same |
| 3e | `/api/teams/red/events` vs `/blue/events` as alice | red streams, blue 404 | As expected | same |
| 4 | alice creates a secret; `GET /api/secrets` | no blue names; admin sees all | demo: secrets disabled. Go test against a real store: as expected | same (now regression-tested) |
| 5a | Team Settings from red's board | `/teams/red/settings` | As expected | same |
| 5b | Cold `/team-settings` as alice | rewritten to a team she is on | **`/teams/admin1/settings`**, the admin's personal team (`config.teams[0]`) | `/teams/alice/settings` (her first own team) |
| 5c | bob opens `/teams/red/settings` | refused or redirected cleanly | **Red's settings page renders** for bob. Saving is refused server-side (`PUT /api/teams/red`, 400 "does not have permission") | unchanged. Follow-up proposed |
| 5d | Repo → Releases → Back → Back → Forward → Forward, then compare deep link, Back, Forward | URL keeps `/teams/red/…`, selector stays Red | As expected | same |
| 5e | Admin → All Teams → Red card quick links | `/teams/red/boards`, `/teams/red/repos` | As expected ("team board", "repository settings"; also "repo settings" → `/teams/red/repos/acme/widgets`) | same |
| 6 | Signed in with no account (`X-Grain-User: mallory` from loopback) creates a team | refused with a visible error | The whole UI is the "access denied" screen ("You do not have access to this server and need to request it"). `POST /api/teams` → 403. The Create Team dialog cannot be reached | same |

## Findings

### F1: cross-team task writes, with the task in the response (fixed)

Reproduction (demo set up as above, alice signed in):

    curl -b alice.jar -X POST $U/api/tasks/3/comments -d '{"body":"alice-was-here"}'   # 200, bob's task JSON
    curl -b alice.jar -X POST $U/api/tasks/3/close -d '{}'                             # 200, task 3 closed

Read scoping (#2415) covered only the GETs. None of the 25 non-GET
routes under `/api/tasks/{id}` checked the caller's team. A member of
any team could comment on, rename, close, retry or move another team's
task, and read it back from the response.

**Fix:** `Server.route` wraps every non-GET `/api/tasks/{id}…` route in
`authorizeTaskRead`. It answers the same 404 as the read, before the
handler runs. Agents and loopback callers carry no human principal and
are unaffected.

### F2: `POST /api/tasks/reorder` returned every team's tasks (fixed)

    curl -b alice.jar -X POST $U/api/tasks/reorder -d '{"ids":["8"]}'   # all tasks, all teams

Reordering one's own task was enough to get the full deployment task
list: titles, descriptions, branches. The IDs it names were not
checked either.

**Fix:** every ID in `ids`, `afterId` and `beforeId` must be readable
(404 otherwise). The response is cut to the caller's teams.

### F3: `GET /api/tasks/states` was not scoped (fixed)

It listed every task ID and state. **Fix:** a scoped caller gets only
their teams' tasks. The cursor still walks every row, so a page may be
shorter than `limit`.

### F4: `GET /api/tasks?team=<foreign>` answered 200 (fixed)

The plan expects 403. The handler ignored `?team=`. This leaked
nothing, but it disagreed with `/api/secrets?team=`. **Fix:** a team
the caller is not on is refused with 403. The list is still not
filtered by `?team=`.

### F5: cold `/team-settings` opened someone else's team (fixed)

`teamSettingsTeam` fell back to `config.teams[0]`, which is the first
team in the deployment (`admin1`). **Fix:** it now prefers the first
team the signed-in user is on (`memberRows`), and falls back to the old
behaviour. Unit test in `ui/src/TeamContext.test.jsx`.

### F6: a non-member can open another team's settings page (fixed later, grain/task-1516)

Bob at `/teams/red/settings` sees Red's settings (profile, members,
config repo, banners), because `GET /api/teams/{id}` and
`/api/config`'s `teams` give every team, with member lists, to every
signed-in user. Writes are refused, but with **400** rather than 403.
Changing who may list teams touches the team picker, proposals and the
multi-team design, so it is reported rather than changed here.

**Fixed in grain/task-1516.** Team metadata is now readable only by
people with a role on the team (viewer included), or by a system admin.
`GET /api/teams/red` answers bob with 403 "you are not a member of team
red". `GET /api/teams` and `/api/config`'s `teams` leave out teams he is
not on, so the team picker and the task, schedule and proposal team
fields only offer teams he can file into. Requests with no human
principal (the CLI, an agent's tool hops) are not narrowed. Team Settings
shows a "not a member" state with buttons for his own teams. The write
refusal was already a 403 by then (2ae042359).

### F7: moving one's own task into a team one is not on (not fixed, follow-up)

`POST /api/tasks/8/move {"team":"blue"}` as alice succeeds, although
she is not on blue. `POST /api/tasks` refuses the same thing ("does not
have permission to submit tasks for team"). After F1, the source team is
checked. The target team is not.

## Fixes and tests

- `pkg/ui/server.go`: team guard on task writes (F1).
- `pkg/ui/tasks.go`: reorder (F2), states (F3), `?team=` (F4).
- `ui/src/TeamContext.jsx`, `App.jsx`, `TeamSettingsOverlay.jsx`:
  cold `/team-settings` (F5).
- `pkg/ui/team_read_scope_test.go`:
  `TestTeamScopeCoversWritesStatesAndSubresources` covers F1–F4,
  attachments and transcripts by URL, and the create-secret response.
  It fails on `main` and passes with the fixes.
- `ui/src/TeamContext.test.jsx`: `teamSettingsTeam` membership
  preference.
- `ui/e2e/team-scoped-reads-walkthrough.spec.js`: the whole walkthrough.
  It fails on `main` at the `?team=blue` 403 and passes with the fixes.

Commands run: `go test ./pkg/ui/` (pass), `npx vitest run
src/TeamContext.test.jsx src/components/TeamSettingsOverlay` (pass),
`npx playwright test e2e/team-scoped-reads-walkthrough.spec.js` (pass),
`npm run format`, `make security` (pass), and grain CI full suite on
the branch.

## Skipped, and why

- **Secrets in the browser.** `grain demo` has no secrets store. The
  create-secret response and `GET /api/secrets` were checked against a
  real `secrets.Store` in the Go test instead.
- **SSE in the browser.** The UI has no SSE client. Live updates were
  checked through its real `If-None-Match` poll, and the team event
  stream through curl.
- **A Create Team error inside the app for a no-account user.** That
  user never gets past the access-denied screen, so the dialog is
  unreachable. The refusal is visible as that screen, and the API
  answers 403.
- **Write routes outside `/api/tasks/{id}`** (schedules, boards,
  sessions and the like) were not audited for team scope. They are
  outside §4.
