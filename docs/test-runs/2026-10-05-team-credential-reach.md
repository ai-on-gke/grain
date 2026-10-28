# A team's GitHub credential decides what its tasks can reach: 2026-10-05

grain/task-1995. This run executes §4 of
[`docs/test-plans/2026-10-05.md`](../test-plans/2026-10-05.md):
- the Team settings → Git Test buttons and `POST /api/teams/{id}/github-credential/check`
  (`b8b5b53ae`, `79c59431e`, `d405b3d41`);
- the refusal at filing and `GET /api/teams/{id}/reachable-repos` (`bdb724dbc`);
- the scoped New task repo pickers (`c067555aa`, `c27456483`);
- parking at clone (`ff0ab25e8`, `949a2b7d4`);
- parking of proposals, schedule firings, suite passes and Run now (`09fd2d550`);
- the schedule's config repo following its team (`9878e11b8`, `ae3e5b46d`).

## Environment and commit

- Tested commit: `candidate` at `795d33a1a`, this branch's base. Each commit above was confirmed
  with `git branch -r --contains <sha>`, and every one lists `origin/candidate` (and `origin/main`).
- Debian sandbox, Go 1.26.6, Node 20.19.0, Playwright 1.63.0 (Chromium), with
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- UI: `cd ui && npm ci && npm run build`, which produces the production bundle in `pkg/ui/static`.
- Server: the real `grain demo -addr 127.0.0.1:0 -open=false -github-api 127.0.0.1:<stub port>` on
  its embedded SQLite store. The spec starts the demo binary that `e2e/global-setup.js` builds.

### What the demo needed first

On `candidate`, `grain demo` wired no GitHub API host and no `ui.Config.GitReach`. In that state:
- filing never asks GitHub, so nothing is ever refused or parked;
- `reachable-repos` answers `available: false`;
- a Test button press goes to `api.github.com`, which a sandbox cannot reach.

So §4 could not be walked against the demo as it stood. This branch adds `-github-api host:port`
(`cmd/grain/demo.go`). The flag:
- accepts loopback only and speaks plain HTTP;
- sets the stored `GitHubHost`/`GitHubInsecureHTTP`;
- wires `gitproxy.Reach{Credentials, API}`, as `daemon.go` does.

It also gives the default team the `default` credential. That is what `MigrateTeamGitCredentials`
does to a deployment's teams on upgrade (task 1592). Without it, the demo's own seeding was refused
with `team "default" has no GitHub credential, so it cannot reach acme/widgets`. That is correct
product behaviour (no deployment fallback since 1592) and only a gap in the demo's seed.

## Stubs

Only GitHub was stubbed:

- **`ui/e2e/githubstub.js`**: a Node HTTP server on `127.0.0.1:0`, standing in for the GitHub
  REST API. It answers `/rate_limit`, `/user` (with `X-OAuth-Scopes` and
  `GitHub-Authentication-Token-Expiration`), `/user/repos` and `/repos/{owner}/{name}`, using
  a per-token table that the spec edits mid-walk to grant access. Its tokens:

  | Token | Behaviour |
  |---|---|
  | `PUSH` (`@octo-dev`) | push `acme/app` and `acme/widgets`; read `acme/lib` and `acme/docs`; nothing on `acme/private` |
  | `NOREACH` (`@octo-limited`) | read `acme/lib` only |
  | `SSO` | 403 with `X-GitHub-SSO: required; url=…` |
  | `LIMITED` | 403 with `X-RateLimit-Remaining: 0` and `X-RateLimit-Reset` |
  | `BAD` | absent from the table, so 401 `Bad credentials` (a revoked token) |
  | the demo's own `ghp_demo_secret_token_acme_widgets` | push `acme/widgets` |

- **Go harness**: `pkg/github/githubsim` (`Sim.Reachable`) in the existing orchestrator tests,
  which this run re-ran and did not change.

Grain's APIs, its React frontend and its store were real throughout. No `page.route` was used.

## Commands

```sh
cd ui && npm ci && npm run build
PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  npx playwright test e2e/team-credential-reach-walkthrough.spec.js
npx vitest run src/components/TeamGitPanel.test.jsx
go test ./pkg/orchestrator -run 'TeamCannotReach|AccessRefused|OtherCloneFailures' -count=1
go test ./pkg/ui -run 'GitHubCredentialCheck|Reach|ConfigRepo' -count=1
go test ./cmd/grain -run 'Demo' -count=1
gofmt -l cmd pkg; (cd ui && npm run format); make security
```

All passed after the fix below. Before the fix, the walkthrough failed at the member step (F1).

## Outcomes

### 1. Test buttons (browser, as team admin `carol`, not a system admin)

On team `platform` (no repos attached), `/teams/platform/settings/git`, with tokens pasted and not
saved:

| Case | What the result panel says |
|---|---|
| revoked/bad | "GitHub rejected the token (bad credentials): it is wrong, revoked or expired" |
| SSO | "the token has not been authorized for this organization's SAML single sign-on; authorize it under the token's "Configure SSO" on GitHub" |
| rate limited | "GitHub is rate limiting this token; try again later" |
| PAT that pushes | "Token accepted as @octo-dev by 127.0.0.1:…", "scopes: repo, workflow", "expires 2026-12-31 00:00:00 UTC" |

- None of the failing cases saved anything. `GET /api/teams/platform` still had no `gitCredential`.
- After Save, the field cleared and the button read Replace. Test then answered `fromReach: true`
  with the whole listing: "✓ acme/app read, push" and "✗ acme/lib read only". `acme/private` had
  no row.
- No token appeared in `page.content()`, or in the body of any `/api/` response the page received,
  after each step and again at the end. This covers all five stub tokens. Each refusal's text was
  also checked not to contain its token.
- A plain member (`dave`): the route answered 403 with and without a pasted value, and the 403 body
  did not echo the token. **The button was not absent**, though (F1, fixed).

### 2. Agreement over five repos (API, team `platform` holding `PUSH`)

| repo | stub | `check` | `reachable-repos` | target | read | write |
|---|---|---|---|---|---|---|
| acme/app | push | push | push | 201 | 201 | 201 |
| acme/widgets | push | push | push | 201 | 201 | 201 |
| acme/lib | read | read | read | 400 | 201 | 400 |
| acme/docs | read | read | read | 400 | 201 | 400 |
| acme/private | none | absent | absent | 400 | 400 | 400 |

There was no disagreement. Every 201 was a task that was not parked. Every 400 named `team
"platform"` and "Team settings → Git", and none carried the token. For a team with repos attached,
an extra write off the team's target list is refused by the target-list rule first ("not on this
deployment's target repo list"). That is why the table uses a team with no repos attached, where
the reach rule is what answers.

### 3. New task pickers (browser)

With Target team set to Platform:
- **Target select**: `—, acme/app, acme/widgets, grain/config-platform, No repo…, Other…`. That is
  push only, plus the config repo.
- **Read-only repos picker**: `acme/app, acme/docs, acme/lib, acme/widgets, grain/config-platform`.
  That is everything readable, and `acme/private` is not offered.

Typing `acme/lib` under Other… showed "Team platform's GitHub credential cannot push to acme/lib
-- give it access on GitHub, or choose another in Team settings → Git." Create task got a 400, and
the form showed it verbatim:

> team "platform"'s GitHub credential "grain-team-cGxhdGZvcm0" cannot push to acme/lib (its target
> repository) -- give the credential access on GitHub, or choose another in Team settings → Git,
> then file this again.

By design (`NewTaskOverlay.jsx`), the pickers are scoped and the warning shown only for a team with
no repos attached. A team with repos is offered those repos and gets no typed-repo warning, and the
server still refuses at filing.

### 4. Parking paths

**In the demo.** The default team's credential was replaced with `NOREACH`. A changed token is
listed again at once. Then:
- schedule `sched-2` Run now (`POST /api/schedules/sched-2/run`) filed one task;
- a hand-started `suite-1` (`POST /api/suite-runs`, two templates, `acme/widgets@main`) filed two.

All three were `awaiting_reply` on the shared comment: "This task was filed, but it cannot be
dispatched yet: team "default"'s GitHub credential … cannot push to acme/widgets … Once the team's
credential can reach it, reply here to have it dispatched." The token was not in it.

Access was then granted in the stub and the first task was replied to. It went to `queued`. The
demo has no dispatcher, so "dispatches and clones" is not observable there.

**In the Go harness.** The two automation-only paths, and the no-sandbox claim, are covered by the
orchestrator tests re-run here (all pass):
- `TestProposedTaskTheTeamCannotReachIsFiledAndParked` (propose_task replay);
- `TestScheduleFiringTheTeamCannotReachIsFiledAndParked`;
- `TestSuitePassTheTeamCannotReachIsFiledAndParked` (a later pass).

Each asserts the task is filed and parked on the shared wording. A parked task is `awaiting_reply`,
which dispatch never offers, so no sandbox is spent.

**Two-minute cache, seen from outside.** Pressing Test on the saved `NOREACH` right after the grant
still showed `✗ acme/widgets` ("not among the repositories GitHub lists this credential as
reaching… refreshed every few minutes"), while the replied task was already `queued`. That is the
documented TTL (`DefaultReachTTL`). The git proxy does not consult that cache at clone time (no
`Reach` use outside `pkg/gitproxy/reach*.go`). So the task clones against GitHub's live answer, and
a stale ✗ does not cost a park.

### 5. Clone-time refusal

`TestAccessRefusedCloneParksAtTheFirstFailure` passes. It covers an access refusal from the proxy
at `prepareCheckout`, which `stopOnAccessRefused` turns into one park on the proxy's own text,
naming the team, the repo and Team settings → Git.

`run.go` records that attempt as one finished, failed run before parking. So one refusal costs one
sandbox and one count of `MaxConsecutiveFailures` (5). It is not retried, and it does not loop.

A reply re-checks the target allowlist (`parkOffAllowlist`) but not reach. If someone replies
without fixing access, the task dispatches again, is refused again, and is parked again, one
sandbox each time. Re-checking at reply would read the up-to-two-minute-old listing and could
re-park a task whose access had just been granted, so the current choice is defensible. It is
recorded here as an observation, not a bug.

### 6. Schedule team switch

Through the API, for a schedule on `grain/config-default` in team `default`:
- `PATCH {team: "platform"}` alone was refused: 400 `only team "default" can submit to
  grain/config-default`;
- `POST /api/schedules` naming team `platform` with `grain/config-default` was refused (400);
- `PATCH {team: "platform", repo: "grain/config-platform"}` saved, and the row reads `platform
  grain/config-platform`.

Moving the repo along with the team is `ScheduleOverlay`'s job on the client, covered by
`SchedulesList.test.jsx`. The overlay itself was not driven in the browser in this run. Yesterday's
finding A (`POST /api/suite-runs` skips the team config-repo check,
`2026-10-04-daily-exploratory-ui.md`) is the suite-run counterpart of these guards. It is already
filed and was not refiled or retested here.

## Findings

### F1: a plain member is offered Save, Test and Remove on the team's GitHub tokens (fixed)

**Reproduction** (deterministic):
1. Start `grain demo -github-api <stub>`.
2. As `admin`, run `POST /api/teams {id: "platform", users: ["carol","dave"], admins: ["carol"]}`.
3. As `carol`, save a token.
4. Open `/teams/platform/settings/git` with `X-Grain-User: dave`.

The token field, Replace and Test are all enabled. Test answers 403 `user "dave" does not have
permission to manipulate team "platform"`, and Replace and Remove do the same. The plan expects
the button to be absent.

**Fix** (`ui/src/components/TeamGitPanel.jsx`, `TeamSettingsOverlay.jsx`):
- `canManageTeamTokens(actor, adminAccounts, team)` mirrors `authorizeTeamCredentialChange`: a
  system admin or one of the team's admins may manage tokens.
- A member sees "A token is saved." or "No token is saved.", then "Only this team's admins can
  save, test or remove it.", for the team token and each per-repo token.
- An unknown viewer keeps the controls, and the server decides.
- The server rule is unchanged.

**Regression tests**:
- `TeamGitPanel.test.jsx`: "offers a plain member no token controls…" and two `canManageTeamTokens`
  cases.
- The member step of `e2e/team-credential-reach-walkthrough.spec.js`.

### F2: the demo could not exercise reach at all (fixed, test infrastructure)

See "What the demo needed first" above. `-github-api` plus `TestDemoGitHubAPIIsLoopbackOnly`.

### Observations (no change)

- Reply after a reach park does not re-check reach (§5 above).
- The saved Test shows a ✗ for up to two minutes after access is granted on GitHub, though filing
  and the clone are unaffected (§4).
- A failed listing (stub down) is not cached: every filing asks again and logs
  `gitproxy: checking whether team …`, and refuses nothing. This is "unknown refuses nothing", seen
  at demo startup with an unreachable `-github-api`.
- Not walked: the "more than 3000 repos" unknown path in the UI.

## Fixes and review

- `c42a1bee6`: `fix(ui)`, F1, frontend only.
- `4e60c41`: the test commit, with `-github-api` in `cmd/grain/demo.go`, `ui/e2e/githubstub.js` and the walkthrough
  spec.

Neither touches `pkg/gitproxy`, `pkg/orchestrator` or `pkg/ui` auth, so no security-review hold is
expected.

## CI verdict

`make security` passed locally (`tests/security` in 126 s), and so did `gofmt -l` (empty) and
`npm run format` (no changes).

Flake seen: grain CI run `39e2dddf2edde31f` (at `3909875`) failed `ui e2e` only in
`e2e/team-connections-models-walkthrough.spec.js:66`, where the `New connection` heading was not
visible within 5 s. This run does not touch that spec or its pane. The same test failed the same
way on grain/task-2006 at `92c72c2` (run `1d6c10767592a9ef`) and passed on that same commit's next
run. Locally it passes against this branch's build (5.3 s). An uncached `retrigger_ci` of this
branch's `01eaf80` (run `dc664fa8e7edacd5`) passed `ui e2e` with 387 passed and 1 skipped. That
confirms a flake. The cached red still gated, so the spec's comment was touched to give the job a
new fingerprint. This recurrence is recorded and was not chased here.

Grain CI (full suite, no `jobs` filter) ran on `4e60c41`, the fix and the walkthrough without this
report. The verdict for the commit that adds this report is the one `wait_for_checks` gives on this
branch's head. It is quoted in the task's closing note, because writing it here would change the
commit it describes.
