# 2026-10-19: interim refactor check 5

Task 4810 (2026-10-19 19:10–20:20Z). This is the fifth rolling check of the
single-tenant reset on `main`, run while the remaining stages are still in
progress. It uses the [fourth check](2026-10-19-interim-refactor-check-4.md)
(task 4801, `main` at 1838be04e) as its template and baseline. Task 4716
does the full qualification after the reset.

## Setup

| | |
|---|---|
| Commit | `main` at **4331ddcab** (merge of task 4804, 3e-iii-c3) when the run started. **`main` moved mid-run** to **e4977c21d** (merge of task 4798, 3e-iv-b1): every row 3e-iv-b1 touches (1, 2, 4–10, 12, 13, 25 and the residue) was re-run on e4977c21d, and the second uncached CI run is on it. Re-fetched before reporting: still e4977c21d |
| Builds | **Google**: `go build ./google/cmd/grain`. **Core**: `go build ./cmd/grain`. Also a Google build with the frontend embedded (`cd ui && npm ci && npm run build`, then the same `go build`) for the browser check |
| Daemons | `grain daemon -data-dir <fresh> -sandbox-backend none -ui-addr 127.0.0.1:<port> -sandbox-port-base <multiple of 1024>`. Google and core on SQLite; Google and core on PostgreSQL 15 (Debian package, local; later starts find the database through `db-config.json`) |
| Pre-reset data | Written by a build of `3e5b6c386`, the commit before stage 1 (4680): one dir `-tenancy multi` (a team `eng` and a registered account `alice`), one dir `-tenancy single`, one multi-tenant Postgres database (team `eng`) |
| Stubs | **forgecli**: a shell script on the Google daemon's `PATH` that logs its calls, prints a token for `auth token`, and answers `api … user` and `api … user/repos` with an HTTP header block and canned JSON (`walker`; `walk-org/service-a`, `walk-org/service-b`). **GitHub**: `pkg/github/githubsim`, inside the e2e harness. The github.com PAT in the seed file is fake (GitHub answers it 401). Nothing of grain's own was stubbed |
| Real | Gemini API (this task's key), `agy` 1.2.2 copied out of `ghcr.io/bwsalmon/grain/grain:main` (`/opt/antigravity/antigravity`), Chromium headless shell (Playwright, `ui/node_modules`) for the UI |
| Host | Task sandbox, Debian 12 |

## Changed since last check (1838be04e → e4977c21d)

Merged to `main`:

| PR | Task | What |
|---|---|---|
| #4198 | 4800 | **Bug D fixed**: the guard no longer calls a pre-reset single-tenant dir "multi-tenant data" (`62617eb7a`) |
| #4199 | 4802 | **Bug E fixed**: `SetAccessCodeForTest` swaps the code through an `atomic.Pointer` (`pkg/ui/server.go`'s `accessCode atomic.Pointer[accessCodeFile]`) |
| #4200 | 4801 | The fourth interim report |
| #4201 | 4803 | **Stage 3e-iii-c2 landed**: every Playwright spec signs in with the demo's access code; the team-role and multi-tenant-trust specs are deleted |
| #4202 | 4811 | `tests/e2e` gives every server the suite starts the run's access code; `grain demo -sensitive-hold` seeds no team or people when single-tenant, and its walkthrough is back |
| #4203 | 4809 | **Bug F fixed**: a start that never served leaves `state/restart-gap.json` alone (`182581e1d`) |
| #4204 | 4804 | **Stage 3e-iii-c3 landed**: `pkg/ui` single-tenant only. `ui.Config.SingleTenant`, `TenancyRecovery`, `IAPConfig.SingleTenant`, `TrustIdentityHeaders`, multi-mode trust in `principal.go`, the 66 `pkg/ui` team tests and the `tests/e2e` team walkthroughs are gone; `tenancyRecovery` is null on `/api/config` |
| #4205 | e1010c47/166 | Grainfile v1 short description and specification (docs, unrelated to the reset) |
| #4206 | 4798 | **Stage 3e-iv-b1 landed (mid-run)**: `grain init` (`pkg/hostinit`) configures the deployment through `/api/deployment` (no `/api/teams` left outside tests); `PUT /api/deployment` now carries `targetRepos`; the repository page's dead team layer and `teamRepoApiRoot` are gone; the Playwright setup uses the deployment routes. 3e-iv-b is now split: **3e-iv-b2** (removing `teams.go`, `/api/teams*` and membership) waits on 3e-iii-c4 and d |

**Still in flight** (from `docs/single-tenant-reset.md` at e4977c21d and
the task branches on the remote; this run had no task-listing tool):
3e-iii-c4 (`-tenancy`, `model/tenancy.go`, the `SingleTenant` switches;
branch `grain/eu/task-4805` has "refactor: remove -tenancy and make the
daemon single-tenant only", not merged), 3e-iii-d (team RBAC), 3e-iv-b2
(`teams.go`, `/api/teams*`, membership, own handlers for the deployment
routes), 3e-v (docs and threat model) and stage 6 part two (team tables,
`SchemaVersion` 20).

## Results

| # | Flow | Result | vs. last check |
|---|---|---|---|
| 1 | Build both mains; boot on fresh dirs (SQLite: Google and core; Postgres: Google and core) | **PASS** on 4331ddcab and e4977c21d. A fresh dir still writes `tenancy` ("single / a new installation, with no accounts or teams"; 3e-iii-c4). Google advertises `forgecli` in `/api/git-ambient-sources`; core advertises `[]`. Postgres dirs hold `db-config.json`, and a restart with no `-postgres-url` reopens the database | same |
| 2 | Access-code sign-in, no username/password | **PASS** (both builds, SQLite and Postgres, both heads). `/` without the code is 401 and serves the "enter access code" page with a single `code` field. A wrong code is 401. The right code, lower-case with a space in it, is 303. With the code, `/api/auth/login`, `/register`, `/change-password`, `/api/users`, `/login`, `/register`, `/google/login` are 404 (without it, 401: the access gate now always runs first, 3e-iii-c3) | same |
| 3 | No team UI anywhere | **PASS in the browser** (4331ddcab). After sign-in the page has no "switch teams", "Team settings", "Team home", "Teams" or "new team" text. `/t/default/tasks` and `/t/eng/tasks` redirect to `/`. No page load calls `/api/teams*`. `/system/models` (→ `/admin/models`) shows `gemini-3.1-pro`; `/system/git` (→ `/admin/git`) shows `bot` and `git.example.test`. 3e-iv-b1 didn't touch the System pane (`TeamGitPanel.jsx` is unchanged on e4977c21d). Text residue: see below | same |
| 4 | No leftover team/admin routes | **FAIL, expected (3e-iii-c4, 3e-iii-d, 3e-iv-b2 not landed)**. `pkg/ui/server.go` still registers 33 `/api/teams…` patterns; `GET /api/teams` is 200. `/api/config` still has `adminAccounts: ["debian"]`, `isAdmin: true` and `teams` (the default team, name `admin`, description "Built-in admin team", with `debian` as member and admin); `tenancyRecovery` is now null (3e-iii-c3). `pkg/model/tenancy.go`, `pkg/ui/team_scope.go` and `pkg/ui/teams.go` are in the tree. `GET /api/deployment` still answers the default team's row (`name: "admin"`, `users`, `admins`, `members`). **Improved (3e-iv-b1):** `pkg/hostinit` has no `/api/teams` call outside its tests | improved (3e-iv-b1) |
| 5 | Guard: refuses a pre-reset multi-tenant dir | **PASS.** Daemon start (both builds, SQLite and Postgres with `-postgres-url`, both heads) exits 1: "this build of grain is single-tenant only, and this data dir was written by a build from before the single-tenant reset: schema 17, from before the single-tenant baseline (schema 19), which grain has no migration from. It needs a fresh data dir… Nothing in this data dir has been changed…". **`diff -r` against a copy taken before the start is now clean** (bug F fixed), and the Postgres `pg_dump` is unchanged. **New finding (bug G):** the same Postgres dir started *without* `-postgres-url` (a pre-reset dir has no `db-config.json`) is refused with the generic "loading the config repository: state repository was written by an older build of grain: repository is at schema 17, this build knows 19", and the dir gains `store/grain.db` | fixed (F); new finding (G) |
| 6 | Guard: pre-reset *single-tenant* dir | **PASS (bug D fixed).** Refused with the same message as row 5, which no longer says "multi-tenant data". `grain state check -migrate <dir>/state-repo` says "this state repository was written by a build from before the single-tenant reset" too. Dir unchanged | fixed |
| 7 | Guard: accepts a fresh dir | **PASS**. Restarts are clean on all four daemons after imports, GCP keys, boards and memory documents, and on the two daemons re-run on e4977c21d | same |
| 8 | Guard: dirs the new build writes itself | **PASS**. `POST /api/teams {name: eng}` is 409 "this daemon is single-tenant (-tenancy=single), and keeps no teams besides default…" on every daemon, and the next start is clean | same |
| 9 | `grain connections import` from a seed | **PASS on SQLite and Postgres**, both heads. Two git entries (github.com PAT `bot` with `token_file`, anonymous `git.example.test` scope `acme`) and one antigravity model connection with a key file give `imported 2 git and 1 model connections`; a second run gives the same message. `secrets/github/bot.token` is 0600 | same |
| 10 | Gemini discovery (live) | **PASS**. 11 models (gemini-3.8/3.7/3.6-flash at high/medium/low, gemini-3.1-pro-high/low) on every daemon and on the imported dir. **Default-model mismatch: still open** (bug 1 of the 10-18 candidate qualification): the seed's `default_model: gemini-3.1-pro`, also the example in `grain connections import -h`, gives `the default agent framework is not ready: no configured agent backend matches team "default", model "gemini-3.1-pro": of 1 configured, 1 not serving that model -- connections are the team's own, so a team admin of "default" adds one in Team settings -> Models & connections` | still failing |
| 11 | PAT / GitHub App against the fake GitHub | **PASS (e2e harness)**. `tests/e2e` (local and CI); `pkg/gitproxy` in CI | same |
| 12 | Command-backed forge via fake `forgecli`, Google build | **PASS** (SQLite and Postgres, both heads). `POST /api/deployment/git-connections {host: depot.code.corp.example, type: ambient, source: forgecli}` is 200. It runs `forgecli auth token`, `forgecli api --include … user` and `forgecli api … user/repos`. The connection reports `ambientLogin: walker`, and `GET /api/deployment/reachable-repos` lists `depot.code.corp.example/walk-org/service-a` and `service-b` (push) | same |
| 13 | `forgecli` in the core build | **PASS**. The same request is a 400 `unknown ambient source "forgecli": gh, git, auto` | same |
| 14 | Per-model pause | **PASS (CI)**. `pkg/orchestrator` and `pkg/cli` in the uncached `go` job | same |
| 15 | Task lifecycle: create (CLI) → approve | **PASS (live)**. On core/SQLite, `grain create -repo acme/widgets -title … -body …` filed task 2 and `grain approve 2` queued it. `grain create -team eng` still exists and fails `team "eng" does not exist` (3e-iv-b2) | same |
| 16 | Lifecycle end to end (sandbox clone, push, PR, grain CI, merge queue, merge) | **PASS (e2e harness)**. Local `tests/e2e`, plus `TestMergeQueue*` and `pkg/daemon` in CI | same |
| 17 | Create through MCP, including proposals | **PASS (e2e harness / CI)** | same |
| 18 | Create through the UI | **PASS (CI)**. `ui e2e` job, uncached, now the access-code suite (3e-iii-c2) | same |
| 19 | Schedules and templates firing | **PASS**. A fresh daemon seeds `system:release-notes`, `system:memory-agent` and `system:infra-review`; the e2e schedule walkthroughs pass | same |
| 20 | Director / interactive chat | **PASS**. A fresh daemon files the director console session (task 1, `awaiting_reply`) | same |
| 21 | State-repo export / import round trip | **PASS (live)**. `grain export -out` of core/SQLite ("exported 10 files … team state repos: 0"), `grain import` into a fresh dir, boot: tasks 1 (awaiting_reply) and 2 (queued), all three git connections, the Gemini connection with its 11 models, the GCP key, the board and the memory document. The imported dir gets a new access code | same |
| 21a | `grain upgrade-postgres` on the baseline | **PASS**. The imported SQLite dir: `-dsn postgres://…` moved 102 tables in 2.4s, wrote `db-config.json` (password in the secret store as `database-password`), and the daemon booted on Postgres with the same tasks | same |
| 22 | `tests/security` route authorization | **PASS** (local and CI), the single-tenant sweep; the route baseline is empty | same |
| 23 | `-tenancy multi` (bug 4757) | **PASS**. `-tenancy multi` on a fresh dir exits 1 on both builds ("…this daemon would run multi-tenant (-tenancy=multi was named on the command line)…"). The refused start still leaves `secrets/`, `state-repo/` and `store/` in the empty path. The flag is still there (3e-iii-c4, branch `grain/eu/task-4805`) | same |
| 24 | Deployment-level GCP keys, boards, memory | **PASS** on core/SQLite. `grain gcp-key set ci-key --project p --account …` gives `gcp-key:ci-key`. `POST /api/boards` is 201. `PUT /api/memory/documents` with a `summary` is 200. All three survive the export/import and the move to Postgres | same |
| 25 | The deployment CLI and routes on `/api/deployment` | **PASS**. `grain git-connections` lists the seed's two connections; `grain default-model gemini-3.1-pro-high` printed "This deployment now runs on gemini-3.1-pro-high by default", and `GET /api/deployment` then had that `defaultModel`. **New (3e-iv-b1, e4977c21d):** `PUT /api/deployment {targetRepos: ["acme/widgets"]}` is 200 and `GET` reads it back, on core/SQLite and Google/Postgres | extended (3e-iv-b1) |

## Previous failures and observations

| Item | Status |
|---|---|
| Team routes, `/api/config`'s `adminAccounts`/`isAdmin`/`teams` (3e-iii/3e-iv) | **Still failing, expected.** See row 4. `pkg/hostinit` no longer calls them (3e-iv-b1); the removal is 3e-iv-b2, after 3e-iii-c4 and 3e-iii-d |
| What `GET /api/deployment` answers | **Unchanged**: the default team's row, `name: "admin"`, `description: "Built-in admin team"`, `configRepo`, `users`, `admins`, `members: [{user: debian, role: admin}]`; now also `targetRepos` once set. Own handlers are 3e-iv-b2 |
| The `-tenancy` flag (3e-iii-c4) | **Still present, expected.** `multi` is refused (row 23). A removal is on `grain/eu/task-4805`, not merged |
| Bug D: the guard calls a pre-reset single-tenant dir "multi-tenant data" | **Fixed** (task 4800). Row 6 |
| Bug E: `SetAccessCodeForTest` races a serving server | **Fixed** (task 4802): the code is an `atomic.Pointer`. `query_test_history` for `TestLocalScriptApprovalIsAnsweredByAPersonAndRecorded`: 2 failures in 121 executions over the last 50 runs, none since the fix; it passed in both uncached runs here |
| Bug F: a refused start rewrites `state/restart-gap.json` | **Fixed** (task 4809). `diff -r` is clean after a refused start, SQLite and Postgres, both builds |
| Default-model mismatch (`gemini-3.1-pro` in the seed and the `import -h` example) | **Still failing** (row 10) |
| `grain git-ambient` with no arguments writes a second host-default github.com connection | **Still present** (both heads). It printed "This deployment now clones, pushes and calls GitHub as this host's own gh login on github.com" and recorded `grain-team-ZGVmYXVsdA-c-…` beside the seed's `bot`; `grain git-connections` lists two github.com connections |
| `GET …/git-connections` reports `status.host: "github.com"` for the anonymous `git.example.test` connection | **Still present** |
| `/api/agent-models` reports agy's "Please sign in to view available models" | **Still present** |
| Help and wording residue | **Still present**: `grain export -h` mentions "team state repositories"; `grain import -h` mentions "running store migrations"; `grain create` has `-team`; `grain gcp-key -h` says `unknown gcp-key subcommand "-h"`; Settings' time-zone field says "Browser default or team default"; the "not ready" log line says "a team admin of \"default\" adds one in Team settings -> Models & connections"; connection names are `grain-team-ZGVmYXVsdA-c-…`; the System pane's Git connections tab says "…other git hosts for this team." and "This team names no target repositories yet. Add them on the Team tab to choose a connection for one." (seen in the browser). **Gone (3e-iv-b1):** the repository page's "team-specific configuration overrides active for this team" / "inheriting global defaults for this team" banner |
| `PUT /api/memory/documents` with no `text` answers 200 with `length: 0` and the document doesn't appear in the list | **Still present** (with a `summary`) |
| A refused `-tenancy multi` start on an empty path creates `secrets/`, `store/`, `state-repo/` | **Still present** (row 23); goes with the flag in 3e-iii-c4 |
| Two daemons on one host collide on the default sandbox ports | Not re-tried; every daemon here had its own `-sandbox-port-base` |
| Stage 6 part two: a baseline-19 store from an earlier post-reset `main` | **Not applicable**: stage 6 part two hasn't landed (`SchemaVersion` is still 19) |

## Full suites

| Suite | Result |
|---|---|
| grain CI, full workflow, **uncached**, on 4331ddcab (`retrigger_ci` `*`, run **ec68e1081de038b6**, on this doc's placeholder at e74057bd9, whose tree is 4331ddcab's plus this file) | **All 11 jobs green.** `go` 11m43s, `ui e2e` 15m40s, `frontend` 5m45s, `installer` 4m18s, `run host` 2m42s, `postgres` 2m28s, `package cache` 2m4s, `hub e2e` 1m45s, `relay image` 1m12s, `terraform` 49s, `lint` 28s. No reds |
| grain CI, full workflow, **uncached**, on e4977c21d (`retrigger_ci` `*`, run **e526894038ea8a15**, at 6ce749edb: e4977c21d merged into this branch, plus the placeholder) | **All 11 jobs green.** `go` 11m55s, `ui e2e` (533 passed, 1 skipped; Playwright 15.1m), `frontend` 5m34s, `installer` 4m32s, `run host` 2m38s, `postgres` 2m23s, `package cache` 2m7s, `hub e2e` 1m40s, `relay image` 1m13s, `terraform` 48s, `lint` 30s. No reds, so no flakes to separate |
| Local `go test -count=1 ./tests/e2e/` (no `-race`), 4331ddcab | **127 pass, 16 skip (live-agent, load, soak and stress tests), 0 fail**, 254s. Last time 136/16/0: 3e-iii-c3 deleted the `tests/e2e` team walkthroughs. 3e-iv-b1 didn't touch `tests/e2e` |
| Local `go test -count=1 ./tests/security/... ./google/...`, e4977c21d | **pass** (`tests/security` 60s; every `google/...` package `ok`, 8 packages) |

## Bugs

Fixed since the last check: **D** (task 4800), **E** (task 4802) and
**F** (task 4809).

Still open: the default-model mismatch (bug 1 of the 10-18 candidate
qualification). Not re-filed; this run had no task-comment tool, and there
is no new evidence beyond row 10.

New this check (proposed as a task by this run, not auto-merge, against
`main`):

- **G. `main` started on a pre-reset Postgres dir without `-postgres-url`
  creates `store/grain.db` and refuses in generic words.** A pre-reset
  build started with `-postgres-url` writes no `db-config.json`. Started
  by `main` with no URL, the daemon opens a fresh SQLite store in the dir
  (`store/grain.db`, schema 19) and then fails loading the config
  repository with `staterepo.ErrSchemaTooOld`: "state repository was
  written by an older build of grain: repository is at schema 17, this
  build knows 19". The guard's reset message is not used, and `diff -r`
  shows `Only in <dir>: store`. With the URL, the same dir is refused
  correctly and left untouched. `grain state check -migrate` already
  uses the reset wording for a schema-17 state repository. Repro: write
  a dir with a build of `3e5b6c386` and `-tenancy multi -postgres-url …`,
  stop it, `cp -a` it, start `main`'s `grain daemon -data-dir <dir> …`
  with no URL, then `diff -rq`. *Owner: stage 7 (4686, landed), next to
  the D and F fixes. Low priority.*

## Observations, not filed

- `GET /api/deployment` still answers the default team's row as it is,
  `members` and all (row 4). 3e-iv-b2 gives the deployment routes
  handlers of their own.
- Without the code, the removed sign-in routes now answer 401 (the access
  gate runs first, on every server, since 3e-iii-c3); with the code they
  are 404. Report #4's 404s were with the code.
- `GET /api/teams` is still 200 with the code; `POST /api/teams` is still
  409 with the `-tenancy=single` wording.

## Brief vs. tree

- The brief says to find in-flight stage tasks with `list_grain_tasks` and
  to comment on open bugs' tasks. This run had neither a task-listing nor
  a task-comment tool, the same as checks #2–#4. In-flight work was read
  from `git log origin/main`, `docs/single-tenant-reset.md` and the
  `grain/eu/task-*` branches on the remote (4805: 3e-iii-c4).
- 3e-iv-b is now split in the plan (b1 landed as task 4798, b2 waits on
  3e-iii-c4 and d). Stage 6 part two hasn't landed, so the
  baseline-19-to-20 check didn't apply.
- Report #4's practical notes held. One more: `grain export` takes
  `-out FILE` (not `-o`), and `grain import` takes the bundle as `-in
  FILE` or as its argument. A pre-reset Postgres dir has no
  `db-config.json`, so give `main` its `-postgres-url` (bug G).
