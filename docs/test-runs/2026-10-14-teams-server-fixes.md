# Teams on the Server: User Delete, Public Credential, Edits, Withdrawn Approvals — Walkthrough 2026-10-14

Plan: `docs/test-plans/2026-10-13.md` §10, steps 1–5. Task 3556 (filed by task 3492).

## Environment

- Branch `grain/eu/task-3556`, cut from `origin/candidate` at `d2f5bb8aa`.
- All five commits under test are on `candidate`. `git branch -r --contains` lists `origin/candidate` and
  `origin/main` for each one: `f221c2fd9`, `f57c4a967`, `4abb33eb3`, `bb1d1e522`, `f575918d9`.
- A real `grain demo -default-target-repo acme/widgets -github-sim -git-proxy -github-sim-public`
  (embedded SQLite), built with `-tags grain_e2e` as `ui/e2e/global-setup.js` builds it. The frontend
  was built with `cd ui && npm ci && npm run build`. `-github-sim-public` is new in this change (see
  Fixes).
- Playwright (chromium) against that demo: `ui/e2e/teams-server-fixes-walkthrough.spec.js`. Every
  non-operator role signs in with its own password, and its context sends no `X-Grain-Admin-Mode`
  (`test.use({ extraHTTPHeaders: {} })`). The system admin is the demo's seeded `admin`; it gets a
  password in setup and turns Admin mode on only where the step says so.
- Before the spec was written, the same steps were walked by hand with `curl` against a separate demo
  on `127.0.0.1:18080`. The findings below came from that pass.
- The sandbox could not download Playwright's browsers (`self-signed certificate in certificate chain`
  from `cdn.playwright.dev`). The ones already cached under `/var/cache/playwright` were used instead
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), the same path CI sets.

## Stubs

| Boundary | Stub |
|---|---|
| GitHub (git half) | `-github-sim`'s bare repo, served by `git http-backend` behind `githubsim.GitUpstream`, behind grain's real git proxy (`-git-proxy`). With `-github-sim-public`, `acme/widgets` answers an anonymous fetch the way a public GitHub repository does. An anonymous push is still a 401 "No anonymous write access." |
| Dispatch of a run | `POST /api/e2e/git-proxy/runs`, the demo's existing stand-in that records the run and mints its sandbox token. |

Nothing of grain's own was stubbed: no API, React component, store or proxy.

## Steps and outcomes

### 1–2. User delete keeps every team administered (`f221c2fd9`)

Setup: `alice` is the only admin of shared team S (with `bob` as a member), and her personal team holds a
task. `bob`'s personal team is empty and has a Director session. Creating a team also makes its creator
an admin, so S's membership is reset to leave `alice` as its only admin.

- **Pass.** System → All Users → Delete `alice` is refused inside the dialog: `cannot delete user
  "alice-…": they are the only admin of teams "Shared …", "alice-…"`. The refusal names both teams, and
  nothing is deleted: the user, S and the personal team all remain.
- **Pass.** After `dave` is made a second admin of S and alice's task is moved to `default` (tasks cannot
  be deleted, and any task on a team counts as "not empty"), the delete lands. Her personal team is gone
  and S keeps `dave` as its admin.
- **Pass.** Delete `bob`: his personal team is gone, and so is its Director session
  (`GET /api/console-sessions`).
- **Pass.** `GET /api/teams` lists no team with an empty `admins`.
- **Finding F1 (fixed).** With more than one team in the refusal, it told the operator "make someone else
  an admin of it, or delete it". It also did not say that a personal team still holding tasks has to be
  emptied first, and that was the actual block on alice's personal team.
- **Finding F4 (fixed).** The regression test named in `f221c2fd9`'s message,
  `TestDeleteUserKeepsEveryTeamAdministered`, does not exist on `candidate` or `main`. Only its doc
  comment remains at the end of `pkg/ui/users_test.go`. The conflict resolution in `096efd2e3` ("both
  regression tests are kept") dropped the function body. So until now CI proved nothing about this
  delete.

### 3. "None (public)" team git credential (`f57c4a967`)

- **Pass.** Team settings → Git identity → Type "None (public)" → "Use no credential" returns 200. After
  a reload the editor still says "Saved: None (public)". `GET /api/teams/{id}` answers
  `gitCredential: "anonymous"`, `hasGitCredential: false`.
- **Pass.** The warning stays: on the team home page, "Connect a git host" (`setup-step-git`) is still
  `data-done="false"`.
- **Pass (with `-github-sim-public`).** The team's run clones `acme/widgets` through grain's git proxy.
  The proxy forwards with no credential and the forge serves the public repo. A push to the run's own
  branch is refused.
- **Finding F2 (fixed).** Without a public repo the clone was refused, and so was the push. The proxy's
  explanation was the one for a token GitHub had rejected: "GitHub refused the credential itself (401) …
  this says nothing about the repository. An expired, revoked or mistyped token looks exactly like
  this". No token was sent, so that sends an operator after a credential that does not exist. A 401 to
  an anonymous request means a credential is needed: the repository is private or missing, or the
  request was a push.
- **Finding F5 (fixed, test environment).** The demo forge had no public repository at all, so the
  plan's "public fetch works" could not be shown against it.

### 4. Team edits and task team moves (`4abb33eb3`)

- **Pass.** Administrator settings → Teams → Edit Team, Members emptied, Admins kept → Save. After a
  page reload the card no longer lists the member, and `GET /api/teams/{id}` lists the two admins and
  nobody else.
- **Pass.** "New board" tasks (repo `grain/config-<team>`):
  - Task Detail → Edit → Target team `(default)` shows "its config repo follows the team: saving moves it
    to grain/config-default", and saving lands `default` / `grain/config-default`.
  - Bulk edit with only the Team row ticked does the same.
  - Bulk edit with the Repo row ticked as well (left on the old config repo) is refused for that task
    with `1 of 1 task(s) failed: only team "<team>" can submit to grain/config-<team>`, and the task is
    unchanged. That is the behaviour the commit describes.

### 5. Withdrawing a cross-team approval (`bb1d1e522`, `f575918d9`)

Producer team P (`requireCrossTeamApproval`, admin `dave`) owns B. Consumer team C (admin `erin`, member
`carol`) owns A, which depends on B. `dave` approves, and A's hold banner goes away.

| Role | Pane | Withdraw |
|---|---|---|
| Dependent team admin (`erin`) | Sees "Team P approved this task's dependency on B" and the Withdraw button | Refused in the pane: `user "erin" does not have permission to manipulate team "P"`; the approval stands |
| Dependent team member (`carol`) | Same | Same refusal |
| Target team admin (`dave`) | **Cannot open A** (`GET /api/tasks/A` is 404; A is another team's), so there is no button | `POST …/withdraw-cross-team-approval` through the API: 200, and the hold returns |
| System admin, Admin mode off | Cannot open A | API 403: "…you are a system admin; turn on Admin mode to do this" |
| System admin, Admin mode on | Sees the approval | Withdraw in the pane: the list goes, and the hold banner with "Approve for team P" is back |

- **Pass.** After a withdrawal, A's detail carries `crossTeamApproval` again. Dispatch waiting for
  re-approval is shown in Go rather than in the demo, which runs no dispatcher. The new
  `TestWithdrawnCrossTeamApprovalHoldsDispatchAgain` approves, withdraws, checks that `dispatch.Cycle`
  skips A, re-approves, and checks that A is then dispatched.
- **Pass.** Withdrawing twice: a second pane, opened before the withdrawal, shows
  `task A has no cross-team approval of its dependency on B` when its button is pressed. The API answers
  404 with the same `error`.
- **Finding F3 (not fixed; proposed as a follow-up).** The target team's admin is the one person besides
  a system admin who may withdraw (or approve), but no UI shows them the button. Both controls live in
  the dependent task's pane, and that task belongs to another team, so they can't open it. Today an
  approval can only be withdrawn from the UI by a system admin in Admin mode, or by the target admin
  through the API. Fixing it means showing the target team, on its own task or team page, the approvals
  it has given and the links waiting for one. That is a cross-team visibility decision (which of another
  team's task IDs and titles the target team may see), so it is out of scope here.
  *Since fixed by task 3773*: the target task's pane lists the other teams' tasks depending on it,
  by ID and team only (not title), with Approve / Withdraw, for that team's admins. The dependent
  pane offers its buttons only to a caller who may press them and tells everyone else whose call it is.
- Minor, not fixed: when a non-system-admin sends `{}` as the body, the answer is 403 "takes an admin of
  the team that owns its target", not "targetTaskId is required". This is deliberate:
  authorization comes before validation.

## Fixes

1. `pkg/model/user_store.go`: `SoleTeamAdminError` says "each of them" for several teams, and that a team
   still holding tasks or schedules has to be emptied first (F1).
2. `pkg/ui/users_test.go`: restored `TestDeleteUserKeepsEveryTeamAdministered` from `c94232837`,
   unchanged. It passes on `candidate` (F4).
3. `pkg/gitproxy/refusals.go`: when GitHub answers 401 to a request sent with no credential, the proxy
   now says GitHub asked for a credential, which only a public fetch can do without. It points a "None
   (public)" team at Team settings → Git identity, and no longer talks about an expired token.
   `IsAccessRefusal` recognises the new wording, so the dispatcher still stops on it at the first
   attempt (F2).
4. `grain demo -github-sim-public` (`cmd/grain/demo.go`, `pkg/demo`): the simulated forge serves its repo
   to anonymous fetches (F5). Default off, so no existing spec changes.

## Tests

- `pkg/ui/users_test.go` `TestDeleteUserKeepsEveryTeamAdministered` (restored).
- `pkg/gitproxy/refusals_test.go` `TestAnUpstream401UnderNoCredentialSaysSo`, extended with the new
  wording, the absence of the expired-token text, and `IsAccessRefusal`.
- `pkg/dispatch/cross_team_dependency_dispatch_test.go` `TestWithdrawnCrossTeamApprovalHoldsDispatchAgain`
  (new).
- `ui/e2e/teams-server-fixes-walkthrough.spec.js` (new): steps 1–5 above against the demo. 5/5 passed
  locally in 22 s.
- No expectation in `tests/security/routes_test.go` was touched.

## CI verdict

grain CI on the branch's final commit: see the pull request. This report is committed with the change,
and the verdict is recorded in the task's closing note.
