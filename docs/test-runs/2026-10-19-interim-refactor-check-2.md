# 2026-10-19: interim refactor check 2

Task 4759 (2026-10-19 01:28–02:35Z). This is the second rolling check of the
single-tenant reset on `main`, run while the remaining stages are still in
progress. It uses the [first check](2026-10-19-interim-refactor-check.md)
(task 4753, `main` at f851da576) as its template and baseline. Task 4716 does
the full qualification after the reset.

## Setup

| | |
|---|---|
| Commit | `main` at **b854e5731** (merge of task 4685, stage 6 part one). Re-fetched before reporting: unchanged |
| Builds | **Google**: `go build a wrapper distribution's `cmd/grain`. **Core**: `go build ./cmd/grain`. Also a wrapper distribution's build with the frontend embedded (`cd ui && npm ci && npm run build`, then the same `go build`) for the browser check |
| Daemons | `grain daemon -data-dir <fresh> -sandbox-backend none -ui-addr 127.0.0.1:<port> -sandbox-port-base <multiple of 1024>`. Google and core on SQLite. **Google and core on PostgreSQL 15** (Debian package, local; last time only Google ran on Postgres) |
| Pre-reset data | Written by a build of `3e5b6c386`, the commit before stage 1 (4680): one dir `-tenancy multi` (a team `eng` and a registered account `alice`), one dir `-tenancy single`, one multi-tenant Postgres database (the same team and account) |
| Stubs | **forgecli**: a shell script on the daemon's `PATH` that logs its calls, prints a token for `auth token`, and answers `api … user` and `api … user/repos` with an HTTP header block and canned JSON (`walker`; `walk-org/service-a`, `walk-org/service-b`). **GitHub**: `pkg/github/githubsim`, inside the e2e harness. The github.com PAT in the seed file is fake (GitHub answers it 401). Nothing of grain's own was stubbed |
| Real | Gemini API (this task's key), `agy` 1.2.2 copied out of `ghcr.io/bwsalmon/grain/grain:main`, Chromium headless shell (Playwright, `ui/node_modules`) for the UI |
| Host | Task sandbox, Debian 12, 4 vCPU |

## Changed since last check (f851da576 → b854e5731)

Merged to `main`:

| PR | Task | What |
|---|---|---|
| #4171 | 4735 | **Stage 3d: sign-in accounts removed.** `/api/users`, password/registration/change-password/Google OAuth routes, the `grain_session` cookie, `grain user`, `Config.PasswordAuthEnabled`/`OpenRegistration`/`GoogleOAuth*`. `grain init` and `scripts/setup.sh` make the installer an admin through `grain settings -add-admin` |
| #4172 | 4743 | **Stage 4c: proposal inheritance, CI identity, executor/proxy teams.** Proposals file into the default team (or the team whose config repo they target). CI runs carry no team. `modelexec`/`modelproxy` lose their team fields |
| #4174 | 4753 | The first interim report |
| #4175 | 4685 | **Stage 6, part one: retired schema dropped.** `access_accounts`, `team_pipeline_pause`, `cross_team_dependency_approval`, the team ceiling columns, legacy agent settings and `capability_teams`. Init drops them from older stores after the guard. `staterepo` refuses a dump that carries multi-tenant rows in the retired schema. `SchemaVersion` stays 18 |

Not landed yet: 3e (teams, team RBAC, `-tenancy`, team UI, route rewrite;
task 4736), 4d (4744), 4e (4745), stage 6 part two (the `user`, `team`,
`team_user`, … tables, which wait on 3e and 4d/4e), stage 7 (4686), and the
fixes for bugs 4756 and 4757. No commit on `main` mentions either bug.

## Results

| # | Flow | Result | vs. last check |
|---|---|---|---|
| 1 | Build both mains; boot on fresh dirs (SQLite: Google and core; Postgres: Google and core) | **PASS**. A fresh dir resolves to `tenancy: single (a new installation, with no accounts or teams…)`. Google advertises `forgecli` in `/api/git-ambient-sources`; core advertises `[]` | same |
| 2 | Access-code sign-in, no username/password | **PASS** (both builds, SQLite and Postgres). `/` without the code returns 401 and a page with a single `code` field. A wrong code is refused (401). The right code, typed lower-case with spaces instead of dashes, returns 303 and the browser lands on `/t/default/tasks`. `/api/auth/login`, `/register`, `/change-password` and `/google/login` now return **404 "no such API endpoint"** (3d deleted them; last time a single-tenant refusal) | same result; routes now gone, not refused |
| 3 | No team UI anywhere | **FAIL, expected: 3e not landed**. The team switcher's "…switch teams here" text, Team home, Team settings and `TeamsPanel` are all still in `ui/src`. **Correction to the last report:** there *is* a New team control. The sidebar's "+ New task" split button opens a menu of `new task, new session, new team, new board`, and New team opens `NewTeamDialog`, which `POST`s `/api/teams` (see bug 4756 below). The item dates from a08e34ecb, before the last check | unchanged; corrected |
| 4 | No leftover team/admin routes | **FAIL, expected: 3e not landed**. Gone since last time (3d): `/api/users` (all methods), `/api/auth/change-password`, `/login`, `/register`, `/google/login`; `/api/config` no longer has `passwordAuthEnabled`, `registrationOpen`, `googleOAuthEnabled`. Still served: `/api/teams` (GET/POST/…), `/api/teams/{id}/…` (git connections, GCP keys, reachable repos, …), `/api/repos/{o}/{n}/admins`. `/api/config` still carries `adminAccounts`, `isAdmin` and `teams`. **`POST /api/teams` still works on a single-tenant daemon (bug 4756)** | improved (3d) |
| 5 | Guard: refuses a pre-reset multi-tenant dir | **PASS**. Daemon start (both builds, SQLite and Postgres) exits 1 at the schema check ("found 17, this build knows 18"). `grain state check -migrate <state-repo>` refuses in the guard's words, naming 2 `team_user` rows and 1 `team` row. New with stage 6: it first warns that `team.max_workers`, `require_cross_team_approval` and the legacy `grain_config` columns "would be ignored". Store and state-repo checksums, and the Postgres `pg_dump`, are unchanged after the refused starts | same |
| 6 | Guard: pre-reset *single-tenant* dir | **PASS** (as designed until stage 7). The start refuses on the schema version; `state check -migrate` says "ok: grain can load this" | same |
| 6a | **New (stage 6):** a dump carrying rows in a dropped table | **PASS**. A copy of the pre-reset single-tenant state repo with one row in `tables/access_accounts.json`: `state check -migrate` refuses "1 access list entries (access_accounts)" in the guard's words | new |
| 7 | Guard: accepts a fresh dir | **PASS**, and restarts are clean (both builds, SQLite and Postgres) | same |
| 8 | Guard: dirs the new build writes itself | **FAIL (bug 4756, still open)**. One `POST /api/teams` on a single-tenant daemon, and the next start exits 1: "this build of grain is single-tenant only… 1 members of teams besides the default one (team_user)… the build that wrote it can still run on it". Reproduced on Google and core, SQLite and Postgres, **and now through the UI in Chromium** (flow 3's New team → Save → `POST /api/teams` 201 → restart refused). The 4757 path changed, see below | still failing; UI repro is new |
| 9 | `grain connections import` from a seed | **PASS on SQLite**: two git entries (github.com PAT `bot` with `token_file`, anonymous `git.example.test` scope `acme`) and one antigravity model connection with a key file: `imported 2 git and 1 model connections`; a second run gives the same message and changes nothing; `secrets/github/bot.token` is 0600. **FAIL on Postgres (new bug A below)**: `grain connections -data-dir <dir> import` on a dir whose daemon runs with `-postgres-url` creates an empty `<dir>/store/grain.db` and refuses "this data dir has no deployment settings yet… start grain daemon on it once". Given `-db-driver postgres -db-dsn <url>` it imports correctly | new failure (Postgres not tried last time) |
| 10 | Gemini discovery (live) | **PASS**. The imported connection discovered 11 models (gemini-3.8/3.7/3.6-flash at high/medium/low, gemini-3.1-pro-high/low) on Google/SQLite, core/SQLite, core/Postgres and the imported dir. **Default-model mismatch: still open.** The seed's `default_model: gemini-3.1-pro` (also the example in `grain connections import -h`) is not a discovered model, so the daemon logs `the default agent framework is not ready: … model "gemini-3.1-pro": of 1 configured, 1 not serving that model` | still failing |
| 11 | PAT / GitHub App against the fake GitHub | **PASS (e2e harness)**. `TestGitHostsPhase5EndToEnd` and the rest of `tests/e2e` (below); `pkg/gitproxy` in CI | same |
| 12 | Command-backed forge via fake `forgecli`, wrapper distribution build | **PASS**. `POST /api/teams/default/git-connections {host: git.example.com, type: ambient, source: forgecli}` runs `GH_HOST=git.example.com forgecli auth token` and `forgecli api --include … user`. The connection says "pushes and API calls act as walker", and reachable repos lists `git.example.com/walk-org/service-a` and `service-b` (push) | same |
| 13 | `forgecli` in the core build | **PASS**. The same request gets a 400 `unknown ambient source "forgecli": gh, git, auto`, and the core daemon never runs `forgecli` | same |
| 14 | Per-model pause | **PASS (CI)**. No run here hit a quota. `pkg/orchestrator` and `pkg/cli` (pause walkthroughs) pass in the uncached `go` job | same |
| 15 | Task lifecycle: create (CLI) → approve | **PASS (live)**. On core `grain create` filed task 2 (proposed), and `grain approve 2` queued it. With `-sandbox-backend none` dispatch logs "admission refused" and nothing runs | same |
| 16 | Lifecycle end to end (sandbox clone, push, PR, grain CI, merge queue, merge) | **PASS (e2e harness)**. `TestStandaloneTaskImplementsTestsAndLandsAChange`, `TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt` and the rest of local `tests/e2e`, plus `TestMergeQueue*` and `pkg/daemon` in CI | same |
| 17 | Create through MCP, including proposals (4c) | **PASS (e2e harness / CI)**. `mcp_propose_task_walkthrough_test.go`, `mcpserver_*`, and 4c's `TestTeamConfigRepoProposalsFileIntoTheRepositorysTeam` / `TestMainConfigRepoProposalsFileIntoTheDefaultTeam` | same |
| 18 | Create through the UI | **PASS (CI)**. `ui e2e` job, uncached | same |
| 19 | Schedules and templates firing | **PASS**. `TestScheduleAutoApproveProposalsWalkthrough` locally. Live: every fresh daemon seeds `system:release-notes`, `system:infra-review` and `system:memory-agent:default` | same |
| 20 | Director / interactive chat | **PASS**. Live: a fresh daemon files the director console session (task 1). The walkthroughs pass in local `tests/e2e` | same |
| 21 | State-repo export / import round trip | **PASS (live)**. `grain export` of core/SQLite (task 2 queued, two git connections, the Gemini connection), `grain import` into a fresh dir, boot: the same task, both git connections, and the Gemini connection with its 11 models. The imported dir gets a new access code | same |
| 22 | `tests/security` route authorization | **PASS** (local and CI). The leftover team routes keep their expected levels; the rewrite is 3e's | same |
| 23 | **New (3d):** what `-tenancy multi` now is | Starts on a fresh dir. Loopback is trusted with no code. Off-loopback (`-ui-addr 0.0.0.0`, called from the host's own non-loopback IP) every API call is 401 "sign in to use this server's API", and an unsigned `X-Grain-User` is refused by name. So with accounts gone, multi only lets in loopback and signed IAP identities. `POST /api/teams` over loopback still makes the dir unstartable (bug 4757, below) | new |

## Previous failures and observations

| Item | Status |
|---|---|
| Team UI and team routes (3e) | **Still failing, expected.** 3e (4736) has not landed. 3d removed the account routes and settings, which is the only change. The New team menu item above was missed last time |
| Bug 4756: single-tenant daemon accepts `POST /api/teams`, then refuses its own dir | **Still failing.** Same on both builds, SQLite and Postgres. **New evidence: the UI reaches it** (sidebar "+ New task" ▾ → New team → Save). The last report said it couldn't |
| Bug 4757: `-tenancy multi` starts, and its first account makes the dir unstartable | **Changed, still failing.** The original trigger (`POST /api/users`) is gone with 3d, so multi can no longer make accounts at all. But `-tenancy multi` still starts, `-tenancy -h` still says "multi is the shared deployment: accounts, sessions, IAP and teams" and that `auto` keeps "a sign-in account, or Google sign-in" multi, and a `POST /api/teams` on it leaves the dir unstartable. 3e (removing `-tenancy`) fixes it outright |
| Default-model mismatch (`gemini-3.1-pro` in the seed and the `import -h` example) | **Still failing.** Unchanged |
| Schema-version message on Postgres ("move the store file out of the way") | **Still present.** Unchanged on both builds. Stage 7 is meant to replace it with the guard's message |
| `/api/agent-models` reports agy's "Please sign in to view available models" | **Still present** |
| Team wording in logs ("a team admin of \"default\" adds one in Team settings"), connection names like `grain-team-ZGVmYXVsdA-c-…` | **Still present** (3e/4c territory) |
| Two daemons on one host collide on the default sandbox ports | **Still present.** The daemon also warns when `-sandbox-port-base` is not a multiple of 256 |

## Full suites on b854e5731

| Suite | Result |
|---|---|
| grain CI, full workflow, **uncached** (`retrigger_ci` `*`, run 52602f4494e4e4dc, on the placeholder of this doc, whose tree is `main`'s plus this file) | **All green except `go`.** `ui e2e` 21m12s, `frontend` 6m57s, `postgres`, `hub e2e`, `installer`, `run host`, `lint`, `package cache`, `relay image`, `terraform` green. **`go` failed** on one `-race` report in `pkg/relay`, below; every other package was `ok` (`tests/e2e` 512s, `tests/security` 970s) |
| `go`, re-run uncached (run 9422101e48c413b4) | **Green**, every package `ok` including `pkg/relay`. With this the full workflow is green on main's tree |
| Local `go test ./tests/e2e/` (no `-race`) | 134 pass, 18 skip (live-agent, load, soak and stress tests), **0 fail**. Last time it was 138. Since then 4743 deleted the proposal-inheritance and per-team executor e2e tests, and 4735 deleted `TestCLIUserVerbsAgainstALiveServer` (five top-level tests) |
| Local `go test ./tests/security/... ./google/...` | pass |

**The `go` red is a flake, not a reset regression.**
`pkg/relay`'s `TestRelayClientSimultaneousUIAndExecutor` hands its
`modelexec.Server` the test's `t.Logf`. The executor connection's goroutine
(`HandleConn` → `unregister` → `s.logf("… disconnected")`) can log after the
test has returned, which the race detector reports against
`testing.(*common).destination`. `query_test_history` has 149 passes and 0
failures for the test and the package. It didn't reproduce locally in
`go test -race -count=30 -run TestRelayClientSimultaneousUIAndExecutor` or
`-count=3` of the whole package. The logging line predates the reset (4743
only dropped the team from a different log line in the same file). grain
marked the job "shared fault" with 4685, 4735, 4734 and 4757. Those
branches' `go` reds are other failures (4757's is in `pkg/orchestrator`'s
log), so that grouping is loose. Filed as its own task (C below).

## Bugs

Still open from the last check: **4756** and **4757** (see above).

New this check (proposed as tasks by this run, not auto-merge, against `main`; they are filed unapproved when the run ends):

- **A. `grain connections import` on a Postgres-backed data dir writes a
  stray SQLite store and refuses with a misleading message.** A daemon
  started with `-postgres-url` (or `-db-driver postgres -db-dsn`) records no
  database config in its data dir, so `daemon.LoadDatabaseConfig` returns
  nothing and `connections import` falls back to SQLite.
  `daemon.OpenStore` then creates `<data-dir>/store/grain.db`, and the import
  refuses "this data dir has no deployment settings yet… start grain daemon
  on it once", which isn't true: the daemon has already started on it. With
  `-db-driver postgres -db-dsn <url>` (or `$GRAIN_DB_DRIVER`/`DSN`) it
  works. Repro: `createdb x`; `grain daemon -data-dir d -postgres-url
  postgres://…/x -sandbox-backend none`; stop it; `grain connections
  -data-dir d import seed.yaml`. *Owner: stage 1 (4680, landed), which added
  `connections import`.* No in-flight stage covers it.
- **C. `pkg/relay` `TestRelayClientSimultaneousUIAndExecutor` races on
  `t.Logf` after the test returns** (the flake above). Test-only.

## Observations, not filed

- `vite build` warns of a duplicate `inputProps` attribute in
  `ui/src/components/RepoWorkOwnership.jsx:119`. The later one wins, so the
  earlier one's props are silently dropped.
- `GET /api/teams/default/git-connections` reports `status.host:
  "github.com"` for the anonymous `git.example.test` connection.
- The default team still lists the daemon's OS user (`debian`) as its admin
  and only member. That's harmless for the guard, which counts members of
  *other* teams only.

## Brief vs. tree

- The brief says to find in-flight stage tasks with `list_grain_tasks`, and
  to comment on an already-open bug's task with new evidence. This run had
  neither a task-listing nor a task-comment tool. In-flight work was read
  from `git log` and `docs/single-tenant-reset.md`, and the new evidence for
  4756 and 4757 is recorded here and in this task's closing note.
- Stage 6 is landing in two parts. 4685's merge is part one, and part two
  waits on 3e and 4d/4e.
