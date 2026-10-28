# 2026-10-19: interim refactor check 4

Task 4801 (2026-10-19 15:10–15:50Z). This is the fourth rolling check of the
single-tenant reset on `main`, run while the remaining stages are still in
progress. It uses the [third check](2026-10-19-interim-refactor-check-3.md)
(task 4766, `main` at 33e88fca9) as its template and baseline. Task 4716
does the full qualification after the reset.

## Setup

| | |
|---|---|
| Commit | `main` at **1838be04e** (merge of task 4792, 3e-iii-c1). Re-fetched before reporting: unchanged, so every row is from this commit |
| Builds | **Google**: `go build ./google/cmd/grain`. **Core**: `go build ./cmd/grain`. Also a Google build with the frontend embedded (`cd ui && npm ci && npm run build`, then the same `go build`) for the browser check |
| Daemons | `grain daemon -data-dir <fresh> -sandbox-backend none -ui-addr 127.0.0.1:<port> -sandbox-port-base <multiple of 1024>`. Google and core on SQLite; Google and core on PostgreSQL 15 (Debian package, local; later starts find the database through `db-config.json`) |
| Pre-reset data | Written by a build of `3e5b6c386`, the commit before stage 1 (4680): one dir `-tenancy multi` (a team `eng` and a registered account `alice`), one dir `-tenancy single`, one multi-tenant Postgres database (the same team and account) |
| Stubs | **ggh**: a shell script on the Google daemon's `PATH` that logs its calls, prints a token for `auth token`, and answers `api … user` and `api … user/repos` with an HTTP header block and canned JSON (`walker`; `walk-org/service-a`, `walk-org/service-b`). **GitHub**: `pkg/github/githubsim`, inside the e2e harness. The github.com PAT in the seed file is fake (GitHub answers it 401). Nothing of grain's own was stubbed |
| Real | Gemini API (this task's key), `agy` 1.2.2 copied out of `ghcr.io/bwsalmon/grain/grain:main` (`/opt/antigravity/antigravity`), Chromium headless shell (Playwright, `ui/node_modules`) for the UI |
| Host | Task sandbox, Debian 12 |

## Changed since last check (33e88fca9 → 1838be04e)

Merged to `main`, all reset stages; no bug fixes landed:

| PR | Task | What |
|---|---|---|
| #4194 | 4791 | **Stage 3e-iii-b landed**: `pkg/cli`, `pkg/daemon`, `pkg/demo` and `tests/e2e` present the access code (`access_code_test.go` in each). `TestSecretResolutionAuditTrailLogging` split from `TestSecretAuditTrailIsTeamAdminOnly`. Test-only |
| #4195 | 4779 | **Stage 3e-iv-a: `/api/deployment` routes**, additive (`pkg/ui/deployment_routes.go`, 13 routes): the deployment, its git connections (list, create, delete, check), agent backends, reachable repos and per-repo config and git connection. They still read and write the default team's row. `grain git-ambient`, `git-connections`, `default-model` and Settings' Models & connections and Git connections editors now call them. 3e-iv is split: **3e-iv-b** (removing `teams.go` and `/api/teams*`) waits on 3e-iii |
| #4197 | 4792 | **Stage 3e-iii-c1: `tests/security` rewritten single-tenant**. `TestRouteAuthorization` sweeps a single-tenant fixture: each route is public or trusted, and a trusted route refuses loopback without the code (with forged admin claims) and answers the code's session. The member/team-admin/system-admin levels and the multi-tenant trust tests are gone, and `.grain/baselines/route-authorization.txt` is empty. 3e-iii-c is split into c1 … c4 (c2 Playwright, c3 `pkg/ui`, c4 the `-tenancy` plumbing) |
| #4196 | 4766 | The third interim report |

**Still in flight** (from `docs/single-tenant-reset.md` at 1838be04e and
the task branches on the remote): 3e-iii-c2 (Playwright presents the
demo's access code; branch `grain/eu/task-4803` has a WIP commit
"sign every spec in with the demo's access code"), 3e-iii-c3 (`pkg/ui`
single-tenant only), 3e-iii-c4 (`-tenancy`, `model/tenancy.go`, the
`SingleTenant` switches), 3e-iii-d (team RBAC), 3e-iv-b (`teams.go`,
`/api/teams*`, membership, `pkg/hostinit`'s team calls), 3e-v (docs and
threat model) and stage 6 part two (team tables, `SchemaVersion` 20).
Fixes for check #3's bugs are in flight: **bug D** on
`grain/eu/task-4800` ("stop calling a pre-reset single-tenant store
multi-tenant", a new `ErrBeforeBaseline`) and **bug E** on
`grain/eu/task-4802` (`SetAccessCodeForTest` through an
`atomic.Pointer`). Neither has merged.

## Results

| # | Flow | Result | vs. last check |
|---|---|---|---|
| 1 | Build both mains; boot on fresh dirs (SQLite: Google and core; Postgres: Google and core) | **PASS**. A fresh dir resolves to `tenancy: single (a new installation…)`. Google advertises `ggh` in `/api/git-ambient-sources`; core advertises `[]`. Postgres dirs hold `db-config.json`, and a restart with no `-postgres-url` reopens the database | same |
| 2 | Access-code sign-in, no username/password | **PASS** (both builds, SQLite and Postgres). `/` without the code is 401. The sign-in page has a single `code` field. A wrong code is 401. The right code, lower-case with a space in it, is 303. `/api/auth/login`, `/register`, `/change-password`, `/api/users`, `/login`, `/register`, `/google/login` are 404 | same |
| 3 | No team UI anywhere | **PASS in the browser.** After sign-in the page has no "switch teams", "Team settings", "Team home", "Teams" or "new team" text. `/t/default/tasks` and `/t/eng/tasks` redirect to `/`. No page load calls `/api/teams*`: the System pane's Models & connections and Git connections tabs now call `GET /api/deployment` and `GET /api/deployment/git-connections` (3e-iv-a), and show the imported `bot`, `git.example.test` and `gemini-3.1-pro`. Text residue: see Observations | same; now on `/api/deployment` |
| 4 | No leftover team/admin routes | **FAIL, expected (3e-iii-c3/c4, 3e-iii-d, 3e-iv-b not landed)**. `pkg/ui/server.go` still registers 33 `/api/teams…` patterns. `/api/config` still has `adminAccounts: ["debian"]`, `isAdmin: true` and `teams` (the default team, name `admin`, with `debian` as member and admin). `pkg/model/tenancy.go`, `pkg/ui/team_scope.go` and `pkg/ui/teams.go` are in the tree. `POST /api/teams` is refused 409 (row 8). New: the `/api/deployment*` routes. `GET /api/deployment` answers the default team's row as it is, including `name: "admin"`, `configRepo`, `users`, `admins` and `members`. Without the code it's 401 | improved (3e-iv-a) |
| 5 | Guard: refuses a pre-reset multi-tenant dir | **PASS.** Daemon start (both builds, SQLite and Postgres) exits 1: "this build of grain is single-tenant only, and this data dir holds multi-tenant data: schema 17, from before the single-tenant baseline (schema 19), which grain has no migration from… Nothing in this data dir has been changed". The Postgres `pg_dump` is unchanged (with the `\restrict` lines removed), and so is the store. **But the refused start does change the data dir**: `state/restart-gap.json` has its `startedAt` rewritten on every refused start, SQLite and Postgres (bug F below). Check #3's checksums didn't catch this | same result; new finding (F) |
| 6 | Guard: pre-reset *single-tenant* dir | **Refused, as stage 7 intends, with the wrong wording (bug D, still open).** Same message as row 5, "holds multi-tenant data", for a dir with no team or account. `grain state check -migrate <dir>/state-repo` says the same. The fix is in flight (task 4800, not merged) | still failing |
| 7 | Guard: accepts a fresh dir | **PASS**. Restarts are clean on all four daemons after imports, GCP keys, boards and memory documents | same |
| 8 | Guard: dirs the new build writes itself | **PASS**. `POST /api/teams {name: eng}` is 409 "this daemon is single-tenant (-tenancy=single), and keeps no teams besides default…" on all four daemons, and the next start is clean | same |
| 9 | `grain connections import` from a seed | **PASS on SQLite and Postgres**. Two git entries (github.com PAT `bot` with `token_file`, anonymous `git.example.test` scope `acme`) and one antigravity model connection with a key file give `imported 2 git and 1 model connections`. A second run gives the same message and changes nothing. `secrets/github/bot.token` is 0600. Postgres dirs need no flags | same |
| 10 | Gemini discovery (live) | **PASS**. The imported connection discovered 11 models (gemini-3.8/3.7/3.6-flash at high/medium/low, gemini-3.1-pro-high/low) on all four daemons and on the imported dir. Discovery runs when the daemon starts, so an import into a running daemon shows the models after the next restart. **Default-model mismatch: still open** (bug 1 of the 10-18 candidate qualification): the seed's `default_model: gemini-3.1-pro`, also the example in `grain connections import -h`, gives `the default agent framework is not ready: no configured agent backend matches team "default", model "gemini-3.1-pro": of 1 configured, 1 not serving that model -- connections are the team's own, so a team admin of "default" adds one in Team settings -> Models & connections` | still failing |
| 11 | PAT / GitHub App against the fake GitHub | **PASS (e2e harness)**. `TestGitHostsPhase5EndToEnd` and the rest of `tests/e2e`; `pkg/gitproxy` in CI | same |
| 12 | Command-backed forge via fake `ggh`, Google build | **PASS, now through `/api/deployment`**. `POST /api/deployment/git-connections {host: depot.code.corp.goog, type: ambient, source: ggh}` is 200. It runs `ggh auth token`, `ggh api --include … user` and `ggh api … user/repos`. The connection reports `ambientLogin: walker` ("pushes and API calls act as walker…"), and `GET /api/deployment/reachable-repos` lists `depot.code.corp.goog/walk-org/service-a` and `service-b` (push) | same |
| 13 | `ggh` in the core build | **PASS**. The same request is a 400 `unknown ambient source "ggh": gh, git, auto` | same |
| 14 | Per-model pause | **PASS (CI)**. `pkg/orchestrator` and `pkg/cli` in the uncached `go` job | same |
| 15 | Task lifecycle: create (CLI) → approve | **PASS (live)**. On core/SQLite, `grain create -repo acme/widgets -title … -body …` filed task 2 (proposed) and `grain approve 2` queued it. `grain create -team eng` still exists and fails `team "eng" does not exist` (3e-iv-b) | same |
| 16 | Lifecycle end to end (sandbox clone, push, PR, grain CI, merge queue, merge) | **PASS (e2e harness)**. The rest of local `tests/e2e`, plus `TestMergeQueue*` and `pkg/daemon` in CI | same |
| 17 | Create through MCP, including proposals | **PASS (e2e harness / CI)** | same |
| 18 | Create through the UI | **PASS (CI)**. `ui e2e` job, uncached | same |
| 19 | Schedules and templates firing | **PASS**. A fresh daemon seeds `system:release-notes`, `system:memory-agent` and `system:infra-review`; the e2e schedule walkthroughs pass | same |
| 20 | Director / interactive chat | **PASS**. A fresh daemon files the director console session (task 1, `awaiting_reply`) | same |
| 21 | State-repo export / import round trip | **PASS (live)**. `grain export` of core/SQLite ("exported 10 files … team state repos: 0"), `grain import` into a fresh dir, boot: tasks 1 and 2 (queued), all three git connections, the Gemini connection with its 11 models, the GCP key, the board and the memory document. The imported dir gets a new access code | same |
| 21a | `grain upgrade-postgres` on the baseline | **PASS**. The imported SQLite dir: `-dsn postgres://…` moved 102 tables in 2s, wrote `db-config.json` (password in the secret store), and the daemon booted on Postgres with the same tasks | same |
| 22 | `tests/security` route authorization | **PASS** (local and CI), **now the single-tenant sweep** (3e-iii-c1). The route baseline is empty | rewritten (3e-iii-c1) |
| 23 | `-tenancy multi` (bug 4757) | **PASS**. `-tenancy multi` on a fresh dir exits 1 on both builds ("…this daemon would run multi-tenant (-tenancy=multi was named on the command line)…"), and a dir whose kept `tenancy` file says `multi` refuses the same way ("kept in …/tenancy"). The flag itself is still there (3e-iii-c4) | same |
| 24 | Deployment-level GCP keys, boards, memory | **PASS** on core/SQLite. `grain gcp-key set ci-key --project p --account …` gives `gcp-key:ci-key` (minter `gcp-key-minter-ci-key`). `POST /api/boards` is 201. `PUT /api/memory/documents` needs a `summary` (400 without one) and is 200 with one. All three survive the export/import and the move to Postgres | same |
| 25 | **New (3e-iv-a):** the deployment CLI on `/api/deployment` | **PASS**. `grain git-connections` lists the seed's two connections; `grain git-ambient` works through the deployment routes. On Google/SQLite, `grain default-model gemini-3.1-pro-high` printed "This deployment now runs on gemini-3.1-pro-high by default", and `GET /api/deployment` then had that `defaultModel`. `pkg/hostinit` still calls `/api/teams/default` (3e-iv-b's) | new |

## Previous failures and observations

| Item | Status |
|---|---|
| Team routes, `/api/config`'s `adminAccounts`/`isAdmin`/`teams` (3e-iii/3e-iv) | **Still failing, expected.** See row 4. 3e-iv-a added the deployment routes beside them; the removal is 3e-iv-b, after 3e-iii-c3/c4 and 3e-iii-d |
| The `-tenancy` flag (3e-iii-c) | **Still present, expected** (3e-iii-c4). `multi` is refused (row 23) |
| Bug D: the guard calls a pre-reset single-tenant dir "multi-tenant data" | **Still failing on `main`**. A fix is on `grain/eu/task-4800`, not yet merged |
| Bug E: `SetAccessCodeForTest` races a serving server (`pkg/ui` `-race` flake) | **Still open on `main`**, helper unchanged. A fix is on `grain/eu/task-4802`, not yet merged. `query_test_history` for `TestLocalScriptApprovalIsAnsweredByAPersonAndRecorded`: 2 failures in 142 executions; it passed in this check's uncached `go` job |
| Default-model mismatch (`gemini-3.1-pro` in the seed and the `import -h` example) | **Still failing** (row 10) |
| `grain git-ambient` with no arguments writes a second host-default github.com connection | **Still present.** With every flag defaulted it printed "This deployment now clones, pushes and calls GitHub as this host's own gh login on github.com" and recorded `grain-team-ZGVmYXVsdA-c-042d8168a1ea` beside the seed's `bot`. `grain git-connections` then lists two github.com connections with no scope. Now through `/api/deployment` |
| `GET …/git-connections` reports `status.host: "github.com"` for the anonymous `git.example.test` connection | **Still present**, now on `GET /api/deployment/git-connections` as well |
| `/api/agent-models` reports agy's "Please sign in to view available models" | **Still present** |
| Help and wording residue | **Still present**: `grain export -h` mentions "team state repositories"; `grain import -h` mentions "running store migrations"; `grain create` has `-team`; `grain gcp-key -h` says `unknown gcp-key subcommand "-h"`; Settings' time-zone field says "Browser default or team default"; the "not ready" log line says "a team admin of \"default\" adds one in Team settings -> Models & connections"; connection names are `grain-team-ZGVmYXVsdA-c-…`. **New:** the System pane's Git connections tab says "Connections to github.com, GitHub Enterprise or other git hosts for this team.", "Which connection each of this team's repositories clones and pushes with…" and "This team names no target repositories yet. **Add them on the Team tab** to choose a connection for one". 3e-ii removed the Team tab, so that last sentence points at nothing (3e-iv-b/3e-v territory) |
| `PUT /api/memory/documents` with no `text` answers 200 with `length: 0` and the document doesn't appear in the list | **Still present** (with a `summary`, which is now required) |
| A refused `-tenancy multi` start on an empty path creates `secrets/`, `store/`, `state/`, `state-repo/` | Not re-checked |
| Two daemons on one host collide on the default sandbox ports | Not re-tried; every daemon here had its own `-sandbox-port-base` |

## Full suites

| Suite | Result |
|---|---|
| grain CI, full workflow, **uncached** (`retrigger_ci` `*`, run **b29d092ce47f635b**, on this doc's placeholder at eca6e118e, whose tree is 1838be04e's plus this file) | **All 11 jobs green.** `go` 11m57s (every package `ok`, including `pkg/relay` 19s, `tests/e2e` 440s, `tests/security` 565s, `pkg/ui` 424s, `pkg/orchestrator` 439s), `ui e2e` 16m47s, `frontend` 5m59s, `installer` 4m24s, `run host` 2m36s, `postgres` 2m16s, `package cache` 2m1s, `hub e2e` 1m44s, `relay image` 1m8s, `terraform` 52s, `lint` 29s. No reds, so no flakes to separate |
| Local `go test -count=1 ./tests/e2e/` (no `-race`), 1838be04e | **136 pass, 16 skip (live-agent, load, soak and stress tests), 0 fail**, 244s. Last time 134/18/0: 3e-iii-b moved two tests from skip to pass |
| Local `go test ./tests/security/... ./google/...`, 1838be04e | pass (`tests/security` 56s; every `google/...` package `ok`) |

## Bugs

Fixed since the last check: none. The fixes for D (task 4800) and E (task
4802) are in flight.

Still open: **D**, **E** and the default-model mismatch (bug 1 of the 10-18
candidate qualification). Not re-filed. This run had no task-comment tool,
so the new evidence below for D's area is filed as its own task, F.

New this check (proposed as a task by this run, not auto-merge, against
`main`):

- **F. A start the pre-reset guard refuses still rewrites
  `state/restart-gap.json`.** The guard's message ends "Nothing in this
  data dir has been changed, so a build from before the single-tenant
  reset can still run on it". The store and the Postgres database are
  unchanged, but `pkg/daemon/run.go` opens the restart-gap recorder
  (`upgrade.OpenGapRecorder(upgrade.GapFile(cfg.DataDir), …)`) before the
  schema guard runs, and it writes the file again when the refused run
  returns: `startedAt` becomes the refused process's start. The pre-reset
  build that runs on the dir afterwards then reads a restart gap measured
  from a process that never served. Repro: write a dir with a build of
  `3e5b6c386`, stop it, copy it (`cp -a dir /tmp/a`), start `main`'s
  `grain daemon -data-dir dir …` (exits 1 in the guard's words), then
  `diff -r /tmp/a dir`: `state/restart-gap.json differ`, only `startedAt`.
  Same on SQLite and Postgres, core and Google builds. *Owner: stage 7
  (4686, landed). Touches the same message as bug D's fix (task 4800).*

## Observations, not filed

- `GET /api/deployment` (3e-iv-a) answers the default team's row as it is:
  `name: "admin"`, a `description` of "The admin team for unassigned tasks
  and schedules", `configRepo: grain/config-default`, `users`, `admins` and
  `members: [{user: debian, role: admin}]`. `POST
  /api/deployment/git-connections` answers the row too, with the
  description "Built-in admin team". The plan has 3e-iv-b give the
  deployment routes handlers of their own, which is where those fields
  should drop out.
- Model discovery runs at daemon start: after `grain connections import`
  into a running daemon, `/api/settings` shows the connection with no
  `discovered` block until the next restart. Check #3 didn't hit this
  because it imported before booting. `connections import -h` doesn't say
  a restart is needed.
- The Git connections tab's "Add them on the Team tab" (residue table
  above) is the only residue that points at a control that no longer
  exists.

## Brief vs. tree

- The brief says to find in-flight stage tasks with `list_grain_tasks` and
  to comment on open bugs' tasks. This run had neither a task-listing nor
  a task-comment tool, the same as checks #2 and #3. In-flight work was
  read from `git log origin/main`, `docs/single-tenant-reset.md` and the
  `grain/eu/task-*` branches on the remote (4800: bug D; 4802: bug E;
  4803: 3e-iii-c2 WIP).
- 3e-iii-c and 3e-iv are now split in the plan (c1 … c4; iv-a, iv-b).
  3e-iii-b, 3e-iii-c1 and 3e-iv-a have landed. Stage 6 part two hasn't, so
  the baseline-19-to-20 check (step 2 of the brief) didn't apply.
- Check #3's practical notes held. Two more: the System pane's tabs are
  reachable directly at `/system/models` and `/system/git` (they redirect
  to `/admin/…`), and a daemon only discovers models at start, so import
  the seed before booting or restart after.
