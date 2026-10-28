# 2026-10-19: interim check of the single-tenant and google/ refactors on main

Task 4753 (2026-10-19 00:00–00:45Z). This is an end-to-end check of what the two refactors have already
changed on `main`, run while single-tenant stages 6 and 7 (4685, 4686) are
still in progress. Task 4716 does the full qualification after them.

## Setup

| | |
|---|---|
| Commit | `main` at **f851da576** (merge of task 4684, stage 5). Re-fetched before reporting: unchanged |
| Builds | **Google**: `go build ./google/cmd/grain`. **Core**: `go build ./cmd/grain`. There is no `nogoogle` build tag. The split is by main package (`google/README.md`), so `-tags nogoogle` builds the same core binary as a plain `go build ./cmd/grain` |
| Daemons | `grain daemon -data-dir <fresh> -sandbox-backend none`, each on its own `-sandbox-port-base`. Google on SQLite, core on SQLite, Google on PostgreSQL 15 (Debian package, local) |
| Pre-reset data | Written by a build of `3e5b6c386`, the commit before stage 1 (4680): one dir `-tenancy multi` (plus a team `eng` and a user `alice`), one dir `-tenancy single`, and one multi-tenant Postgres database |
| Stubs | **forgecli**: a shell script on the daemon's `PATH` that logs its calls, prints a token for `auth token`, and answers `api … user` and `api … user/repos` with an HTTP header block and canned JSON (`walker`; `walk-org/service-a`, `walk-org/service-b`). **GitHub**: `pkg/github/githubsim`, inside the e2e harness. The github.com PAT in the seed file is fake. Nothing of grain's own was stubbed |
| Real | Gemini API (this task's key), `agy` 1.2.2 copied out of `ghcr.io/bwsalmon/grain/grain:main`, Chromium (Playwright 1.64) for the UI |
| Host | Task sandbox, Debian 12, 4 vCPU, 7.8 GiB |

## What has actually landed

The brief lists stage 3 as "accounts, sessions, RBAC, admin mode and the
multi-tenant UI removed", and stage 4 as all team plumbing. According to
`docs/single-tenant-reset.md` and the tree, only some slices have landed:

- **Landed:** 3a (access lists, team pipeline pauses, 4682), 3b (cross-team
  approval, team events, 4733), 3c (admin mode, 4734), 4a (team capability
  scoping, 4683), 4b (team limits, 4742), and stage 5 (the guard, 4684).
- **Not landed:** 3d (password and Google accounts, sessions), 3e (teams,
  membership, team RBAC, the `-tenancy` flag, the team switcher and
  team-in-URL UI), 4c (per-team CI identity, proposal inheritance), 4d
  (team-scoped memory, boards, custom fields) and 4e (team GCP keys).

So the team UI and the `/api/teams` and `/api/users` routes are still there,
as expected for unlanded slices. Two of them now cause real trouble because
the guard *has* landed (bugs 1 and 2 below).

## Results

| # | Flow | Result |
|---|---|---|
| 1 | Build both mains; boot on fresh dirs (SQLite: Google and core; Postgres: Google) | **PASS**. A fresh dir resolves to `tenancy: single (a new installation…)`. The Google build advertises `forgecli` in `/api/git-ambient-sources`; core advertises nothing |
| 2 | Access-code sign-in, no username/password | **PASS** (both builds and Postgres). `/` without the code returns 401 and a page with a single masked `code` field and no username field. A wrong code is refused. The right code, typed lower-case with spaces, lands on `/t/default/tasks`. `/api/auth/login`, `/register` and `/google/login` return 404 "this daemon is single-tenant" |
| 3 | No team UI anywhere | **FAIL, expected: 3e not landed**. The sidebar has Team home, Team settings (with members) and User settings. The team switcher says "Teams own their repositories, credentials and model connections; switch teams here", and `/admin` lists "all teams" and "all users". There is no New team button (removed by 4b). Not filed: this is 3e's scope |
| 4 | No leftover team/admin routes | **FAIL, expected: 3d/3e not landed**. Still served: `/api/teams` (GET/POST/PUT/DELETE), `/api/teams/{id}/…` (git connections, agent backends, GCP keys, audit logs, repo config), `/api/users` (CRUD), `/api/repos/{o}/{n}/admins`, `/api/auth/change-password`. `/api/config` and `/api/settings` still carry `passwordAuthEnabled`, `openRegistration`, `googleOAuth*` and `adminAccounts`. **`POST /api/teams` works on a single-tenant daemon, and that data dir then won't start (bug 1)** |
| 5 | Guard: refuses a pre-reset multi-tenant dir | **PASS**. Daemon start (SQLite and Postgres) exits 1 at the schema check ("found 17, this build knows 18") before the guard. `grain state check -migrate` on the state repo, which is the upgrade rehearsal, refuses with the guard's message, naming the `team_user` and `team` rows. After moving the store aside as the schema error suggests, the start still refuses on the state repo's old schema. Nothing was changed in either dir |
| 6 | Guard: pre-reset *single-tenant* dir | **PASS** (as designed until stage 7). The start refuses on the schema version; `state check -migrate` says "ok: grain can load this" |
| 7 | Guard: accepts a fresh dir | **PASS**, and restarts are clean |
| 8 | Guard: dirs the new build writes itself | **FAIL (bugs 1, 2)**. One `POST /api/teams` on a single-tenant daemon, or one `POST /api/users` on `-tenancy multi`, and the next start exits 1 with "this build of grain is single-tenant only" (SQLite and Postgres) |
| 9 | `grain connections import` from a JSON seed | **PASS**. Two git entries (github.com PAT `bot` with `token_file`, anonymous `git.example.test` scope `acme`) and one antigravity model connection with a key file: `imported 2 git and 1 model connections`. A second run is a no-op. After restart, the connections are listed at deployment level and `secrets/github/bot.token` is 0600 |
| 10 | Gemini discovery (live) | **PASS**. The imported connection discovered 11 models (gemini-3.8/3.7/3.6-flash at high/medium/low, gemini-3.1-pro high/low). **Known, still open:** the seed's `default_model: gemini-3.1-pro` (also the example in `grain connections import -h`) names no discovered model, so the daemon logs "the default agent framework is not ready". This is bug 1 of the 10-18 candidate qualification |
| 11 | PAT / GitHub App against the fake GitHub | **PASS (e2e harness)**. `TestGitHostsPhase5EndToEnd`, `TestTeamOwnedGitHubPATScopingAndGitProxyIsolationWalkthrough`, `connections_seed_test.go`, `credential_refusal_test.go`, `ambient_credential_remote_test.go`; and `pkg/gitproxy`'s App/PAT tests (43 matched and passed) |
| 12 | Command-backed forge via fake `forgecli`, Google build | **PASS**. Adding `depot.code.corp.example` with ambient source `forgecli` makes the daemon run `GH_HOST=depot.code.corp.example forgecli auth token` and `forgecli api --include … user`. The connection shows "pushes and API calls act as walker", and reachable repos lists `depot.code.corp.example/walk-org/service-a` and `service-b` (push) |
| 13 | `forgecli` in the core build | **PASS**. With the same `forgecli` on `PATH`, `source: forgecli` gets a 400 "unknown ambient source \"forgecli\": gh, git, auto", and `forgecli` is never executed |
| 14 | Per-model pause | **PASS (tests)**. A pause trips only when a model reports a quota, which no run here hit. `pkg/cli` `connection_pause_walkthrough_test.go` and `pkg/orchestrator` `pause_model_test.go` pass (29 orchestrator Pause tests), as does `TestWalkthroughAShortPauseExpiresAndTheQueuedTaskDispatches` |
| 15 | Task lifecycle: create (CLI) → approve → dispatch | **PASS (live)**. On the core daemon `grain create` filed task 2 (proposed), and `grain approve 2` queued it. With `-sandbox-backend none` it was not dispatched |
| 16 | Lifecycle end to end: sandbox clone, push, PR, grain CI checkout with the deployment connection, merge queue, merge | **PASS (e2e harness)**. `TestStandaloneTaskImplementsTestsAndLandsAChange` (`grain init workstation`, a single-tenant daemon, githubsim) passed in 33s. Its first local run failed only because one of this check's own daemons held the default sandbox ports. Also passed: `TestDispatchPreClonesTheRepoSoTheAgentNeverHasTo`, `TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt`, `TestProposedTaskWaitsForApprovalThenRunsThroughTheCLI`, `TestCIToolsWalkthroughRecheckOnlyTheFailedJob`, every `TestMergeQueue*`, and `pkg/daemon`'s `TestCi*` / `TestBuildCIService*` (CI job checkout with the deployment's connection) |
| 17 | Create through MCP | **PASS (e2e harness)**. `mcp_propose_task_walkthrough_test.go`, `mcpserver_*` |
| 18 | Create through the UI | **PASS (CI)**. `ui e2e` job (Playwright over a real `grain demo`) |
| 19 | Schedules and templates firing | **PASS (e2e harness)**. `TestScheduleAutoApproveProposalsWalkthrough` and the three `TestMultiTeam*` template/suite/schedule tests (rewritten by 4700). Live: a fresh daemon seeds `system:release-notes`, `system:infra-review` and `system:memory-agent:default` |
| 20 | Director / interactive chat | **PASS (e2e harness)**. `TestWalkthroughAMessagePostedWhileTheRunAnswersIsDispatchedFor` and the `pause_chat` walkthrough. Live: a fresh daemon files the director console session (task 1). `TestLiveBranchFixInteractiveQuestionHandling` is a live-agent test and skips without its credentials |
| 21 | State-repo export / import round trip | **PASS (live)**. `grain export` of the core daemon (task 2 queued, three git connections, the Gemini connection), `grain import` into a fresh dir, boot: the same task and state, all three git connections, and the Gemini connection with its 11 discovered models. The access code is new on the imported dir (`local-api-token` is not in the bundle) |
| 22 | `tests/security` route authorization | **PASS** (local and CI). It passes because the leftover routes keep their expected levels; the single-tenant route rewrite belongs to 3e |

## Full suites on f851da576

| Suite | Result |
|---|---|
| grain CI, full workflow, **uncached** (`retrigger_ci` `*`, run 7c18a924a99d6e1e on this doc commit): `go` (`go test -race ./...`, which includes `tests/e2e`, `tests/security`, `google/...` and `pkg/model/guardtest` via the store tests), `frontend` (`npm test`), `ui e2e` (Playwright), `postgres`, `hub e2e`, `installer`, `run host`, `lint`, `package cache`, `relay image`, `terraform` | **All green.** `go` 21m26s, `ui e2e` 661 passed and 1 skipped (Chromium and WebKit), `frontend` 7m25s, every other job green. No reds, so there were no flakes to sort from new failures |
| Local `go test ./tests/e2e/` (no `-race`) | 138 pass, 18 skip (live-agent, load, soak and stress tests that need credentials or `-long`), 1 fail, the port collision above. That test passed on rerun |
| Local `go test ./tests/security/... ./google/...` | pass |

## Bugs filed (from task 4753, against `main`)

1. **A single-tenant daemon accepts `POST /api/teams`, then refuses to start
   on its own data dir.** The guard counts the new team's `team_user` row.
   Undoing it means editing the database by hand, because `DELETE /api/teams`
   needs the daemon running. It reproduces on both builds, SQLite and
   Postgres. The refusal also says "the build that wrote it can still run on
   it", which is false here: this build wrote it. *Cause: the guard (stage 5)
   landed before 3e removed the team-writing routes.* The UI cannot reach it
   (no New team button); the API and CLI can.
2. **`-tenancy multi` still starts on main, and its first account makes the
   dir unstartable.** It serves `/api/users` and password sign-in. One
   `POST /api/users`, and the next start is refused as "single-tenant only".
   *Cause: 3d/3e not landed.* `-tenancy auto` picks single on a fresh dir, so
   this needs the flag passed explicitly, but the flag's help still offers
   multi.

Neither blocks stages 6 or 7. Both should be fixed (most simply by 3e)
before a release is cut from `main`.

## Observations, not filed

- The schema-version refusal for a pre-reset dir says to "move the store file
  out of the way", even on Postgres, where there is no store file. It does
  not say the build is single-tenant only. `docs/single-tenant-reset.md`
  folds the old schema version into the guard once stage 7 lands, which
  should replace this message.
- Team wording in logs and the UI ("a team admin of \"default\" adds one in
  Team settings", "You're working in this team"), and git connection names
  like `grain-team-ZGVmYXVsdA-c-…` (the base64 of `default`). This is 3e/4c
  territory.
- `/api/agent-models` reports agy's own "Please sign in to view available
  models", even though the connection's key-based discovery works.
- Two daemons on one host collide on the default sandbox ports
  (9728–9983) unless one is given `-sandbox-port-base`.

## Brief vs. tree

- "`-tags nogoogle` core build": there is no such tag. Core is
  `./cmd/grain` and Google is `./google/cmd/grain`.
- "Stage 3 … accounts, sessions, RBAC … multi-tenant UI removed": only 3a–3c
  have landed (see above). Stage 4 has landed 4a–4b.
