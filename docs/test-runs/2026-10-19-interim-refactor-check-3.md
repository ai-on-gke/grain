# 2026-10-19: interim refactor check 3

Task 4766 (2026-10-19 06:57–07:50Z). This is the third rolling check of the
single-tenant reset on `main`, run while the remaining stages are still in
progress. It uses the [second check](2026-10-19-interim-refactor-check-2.md)
(task 4759, `main` at b854e5731) as its template and baseline. Task 4716
does the full qualification after the reset.

## Setup

| | |
|---|---|
| Commit | `main` at **a1eef38dc** (merge of task 4786) when the run started. Re-fetched before reporting: `main` had moved to **33e88fca9** (merge of task 4777, **stage 3e-ii: the multi-tenant UI removed**). The browser check, bug 4756, `connections import`, the pre-reset guard and 4757 were re-run on 33e88fca9 (rows 3, 5, 8, 9 and 23); everything else is from a1eef38dc. This branch's final CI run, on top of 33e88fca9, covers that tree |
| Builds | **Google**: `go build ./google/cmd/grain`. **Core**: `go build ./cmd/grain`. Also a Google build with the frontend embedded (`cd ui && npm ci && npm run build`, then the same `go build`) for the browser check, on both commits |
| Daemons | `grain daemon -data-dir <fresh> -sandbox-backend none -ui-addr 127.0.0.1:<port> -sandbox-port-base <multiple of 1024>`. Google and core on SQLite; Google and core on PostgreSQL 15 (Debian package, local) |
| Pre-reset data | Written by a build of `3e5b6c386`, the commit before stage 1 (4680): one dir `-tenancy multi` (a team `eng` and a registered account `alice`), one dir `-tenancy single`, one multi-tenant Postgres database (the same team and account) |
| Stubs | **forgecli**: a shell script on the Google daemon's `PATH` that logs its calls, prints a token for `auth token`, and answers `api … user` and `api … user/repos` with an HTTP header block and canned JSON (`walker`; `walk-org/service-a`, `walk-org/service-b`). **GitHub**: `pkg/github/githubsim`, inside the e2e harness. The github.com PAT in the seed file is fake (GitHub answers it 401). Nothing of grain's own was stubbed |
| Real | Gemini API (this task's key), `agy` 1.2.2 copied out of `ghcr.io/bwsalmon/grain/grain:main` (`/opt/antigravity`), Chromium headless shell (Playwright, `ui/node_modules`) for the UI |
| Host | Task sandbox, Debian 12, 4 vCPU |

## Changed since last check (b854e5731 → a1eef38dc → 33e88fca9)

Merged to `main`, reset stages and bug fixes:

| PR | Task | What |
|---|---|---|
| #4181 | 4736 | **Stage 3e-i: `grain team` CLI removed.** Replaced by deployment-level `grain git-ambient`, `git-connections`, `default-model` and `gcp-key` (`pkg/cli/deployment.go`), still reaching `/api/teams/default/...` until 3e-iv. 4736 split the rest of 3e into a chain (3e-ii … 3e-v) |
| #4192 | 4778 | **Stage 3e-iii-a**: `pkg/ui`'s real-listener tests present the access code. Test-only |
| #4193 | 4777 | **Stage 3e-ii: the multi-tenant UI removed** (team switcher, `/t/<team>` routing, team settings overlay, Teams pane, team home, executor-token team picker); git and model connections editors move into Settings. *Landed during this run, at 33e88fca9* |
| #4178 | 4744 | **Stage 4d**: memory, boards and custom fields are deployment-level |
| #4187 | 4745 | **Stage 4e**: GCP keys are deployment-level `gcp-key:<name>`. Stage 4 is landed |
| #4182 | 4686 | **Stage 7: schema squashed to one baseline** (`BaselineSchemaVersion` 19). No migrations; the guard refuses every pre-reset version before counting rows. Landed ahead of 3e and stage 6 part two |
| #4176 | 4756 | **Bug 4756 fix**: the store refuses a team or account the single-tenant guard would refuse |
| #4177 | 4757 | **Bug 4757 fix**: the daemon refuses to start multi-tenant |
| #4185 | 4764 | **Check #2's bug A fix**: a Postgres daemon records its database in the data dir (`db-config.json`), so `connections import` finds it |
| #4180 | 4765 | **Check #2's bug C fix**: `TestRelayClientSimultaneousUIAndExecutor` waits for executor handlers |
| #4184 | 4767 | `grain init` makes a fresh install single-tenant |
| #4179 | 4759 | The second interim report |

Also merged, not part of the reset: persistent sandboxes with host-port and
`/staging/` routing (4786, with a `/staging/` CSP fix), persistent sandboxes
on `GET /api/sandboxes` and in the UI (4787), merged/merging task colours,
Cloudtop installs with no git connection (4782), the Cloudtop default model
`gemini-pro-coordinator` (4769), `corp-research` on by default for fresh
Google installs (4771), and a monorepo workspaces evaluation doc.

**Still in flight**, from `docs/single-tenant-reset.md` at 33e88fca9:
3e-iii-b (test preparation in `pkg/cli`, `pkg/daemon`, `pkg/demo`,
`tests/e2e`, `tests/security`), 3e-iii-c (the `-tenancy` flip and the
route rewrite), 3e-iii-d (team RBAC), 3e-iv (`teams.go` and the
`/api/teams*` routes), 3e-v (multi-tenant docs, threat model, stage 3
marked landed), and stage 6 part two (`user`, `team`, `team_user`, … and
the team columns; `SchemaVersion` 20). This run had no task-listing tool,
so their task ids aren't known here.

## Results

| # | Flow | Result | vs. last check |
|---|---|---|---|
| 1 | Build both mains; boot on fresh dirs (SQLite: Google and core; Postgres: Google and core) | **PASS**. A fresh dir resolves to `tenancy: single (a new installation…)`. Google advertises `forgecli` in `/api/git-ambient-sources`; core advertises `[]`. A Postgres data dir now holds `db-config.json` (4764) | same |
| 2 | Access-code sign-in, no username/password | **PASS** (both builds, SQLite and Postgres). `/` without the code is 401 with a single `code` field, posting to `/api/auth/access-code`. A wrong code is 401. The right code, lower-case with spaces, is 303. `/api/auth/login`, `/register`, `/change-password`, `/api/users`, `/login`, `/register`, `/google/login` are 404 | same |
| 3 | No team UI anywhere | **At a1eef38dc: FAIL, expected.** The switcher's "switch teams" text was still there, and "+ New task" ▾ (Create options) still listed `new task, new session, new team, new board`. **At 33e88fca9 (3e-ii): PASS in the browser.** After sign-in the page has no "switch teams", "Team settings", "Team home" or "Teams" text. Create options lists `new task, new session, new board`. `/t/default/tasks` and `/t/eng/tasks` redirect to `/`. No page load calls `/api/teams*`. `ui/src` still carries `NewTeamDialog.jsx` and the other `Team*.jsx` components until 3e-iv/3e-v | **fixed by 3e-ii** (landed during this run) |
| 4 | No leftover team/admin routes | **FAIL, expected (3e-iv not landed)**. `/api/teams` (31 routes, down from 36), `/api/repos/{o}/{n}/admins` and `/api/config`'s `adminAccounts`, `isAdmin` and `teams` are still served. `pkg/model/tenancy.go` and `pkg/ui/team_scope.go` are still in the tree. `GET /api/teams` lists `default` with the OS user `debian` as its admin. `POST /api/teams` is now refused (row 8). New deployment routes exist alongside: `/api/gcp-keys`, `/api/memory*` and `/api/boards*` | improved (4d, 4e) |
| 5 | Guard: refuses a pre-reset multi-tenant dir | **PASS, new message.** Daemon start (both builds, SQLite and Postgres) exits 1 in the guard's words: "this build of grain is single-tenant only, and this data dir holds multi-tenant data: schema 17, from before the single-tenant baseline (schema 19), which grain has no migration from… Nothing in this data dir has been changed". `grain state check -migrate` refuses the state repo with the same text. Store and state-repo checksums are unchanged after the refused starts. The Postgres `pg_dump` is unchanged except pg_dump's random `\restrict` token. Before stage 7 the start refused on "found 17, this build knows 18". Re-checked at 33e88fca9 | same result; message improved (stage 7) |
| 6 | Guard: pre-reset *single-tenant* dir | **Refused, as stage 7 intends.** The start and `state check -migrate` now refuse it like row 5. Last time it was "ok: grain can load this". The wording calls this dir "multi-tenant data", which it isn't (bug D below) | changed by design (stage 7); new wording bug |
| 6a | A dump carrying rows in a dropped table | **PASS, now redundant.** Stage 7 removed `retired.go`. Any pre-reset dump is refused on its version before a row is read | changed by design |
| 7 | Guard: accepts a fresh dir | **PASS**, restarts are clean (both builds, SQLite and Postgres, after imports, GCP keys, boards and memory documents) | same |
| 8 | Guard: dirs the new build writes itself | **PASS (bug 4756 fixed).** `POST /api/teams` on a single-tenant daemon is **409** "this daemon is single-tenant (-tenancy=single), and keeps no teams besides default: this build would refuse to start on a data dir holding one (team \"eng\")", and the next start is clean. Same on Google and core, SQLite and Postgres, at a1eef38dc and 33e88fca9. **Through the UI at a1eef38dc**: Create options → New team → Save sent `POST /api/teams` 409, and the dialog showed the refusal; the restart was clean. At 33e88fca9 the item is gone | **fixed** |
| 9 | `grain connections import` from a seed | **PASS on SQLite and Postgres (check #2's bug A fixed).** Two git entries (github.com PAT `bot` with `token_file`, anonymous `git.example.test` scope `acme`) and one antigravity model connection with a key file give `imported 2 git and 1 model connections`. A second run gives the same message and changes nothing. `secrets/github/bot.token` is 0600. On a dir whose daemon ran with `-postgres-url`, no stray `store/` is created and no flags are needed. Re-checked at 33e88fca9 | **fixed** on Postgres |
| 10 | Gemini discovery (live) | **PASS**. The imported connection discovered 11 models (gemini-3.8/3.7/3.6-flash at high/medium/low, gemini-3.1-pro-high/low) on all four daemons and on the imported dir (`GET /api/settings` → `agentBackends[].discovered`). **Default-model mismatch: still open** (bug 1 of the 10-18 candidate qualification). The seed's `default_model: gemini-3.1-pro`, also the example in `grain connections import -h`, gives `the default agent framework is not ready: … model "gemini-3.1-pro": of 1 configured, 1 not serving that model` | still failing |
| 11 | PAT / GitHub App against the fake GitHub | **PASS (e2e harness)**. `TestGitHostsPhase5EndToEnd` and the rest of `tests/e2e`; `pkg/gitproxy` in CI | same |
| 12 | Command-backed forge via fake `forgecli`, Google build | **PASS**. `POST /api/teams/default/git-connections {host: depot.code.corp.example, type: ambient, source: forgecli}` runs `GH_HOST=depot.code.corp.example forgecli auth token` and `forgecli api --include … user`. The connection reports `ambientLogin: walker` ("pushes and API calls act as walker…"), and `reachable-repos` lists `depot.code.corp.example/walk-org/service-a` and `service-b` (push) | same |
| 13 | `forgecli` in the core build | **PASS**. The same request is a 400 `unknown ambient source "forgecli": gh, git, auto`, and core never runs `forgecli` | same |
| 14 | Per-model pause | **PASS (CI)**. `pkg/orchestrator` and `pkg/cli` in the uncached `go` job | same |
| 15 | Task lifecycle: create (CLI) → approve | **PASS (live)**. On core, `grain create -repo acme/widgets -title … -body …` filed task 2 (proposed) and `grain approve 2` queued it. `grain create` still has a `-team` flag (`team "eng" does not exist`), 3e-iv's | same |
| 16 | Lifecycle end to end (sandbox clone, push, PR, grain CI, merge queue, merge) | **PASS (e2e harness)**. The rest of local `tests/e2e`, plus `TestMergeQueue*` and `pkg/daemon` in CI | same |
| 17 | Create through MCP, including proposals | **PASS (e2e harness / CI)** | same |
| 18 | Create through the UI | **PASS (CI)**. `ui e2e` job, uncached | same |
| 19 | Schedules and templates firing | **PASS**. `TestScheduleAutoApproveProposalsWalkthrough` locally. A fresh daemon seeds `system:release-notes`, `system:infra-review` and **`system:memory-agent`** (4d dropped the `:default` suffix) | same |
| 20 | Director / interactive chat | **PASS**. A fresh daemon files the director console session (task 1) | same |
| 21 | State-repo export / import round trip | **PASS (live)**. `grain export` of core/SQLite (task 2 queued, two git connections, the Gemini connection), `grain import` into a fresh dir, boot: the same tasks, both git connections, and the Gemini connection with its 11 models. The imported dir gets a new access code | same |
| 21a | **New (stage 7 kept it):** `grain upgrade-postgres` on the baseline | **PASS**. The imported SQLite dir: `-dsn postgres://…` moved 102 tables in 2s, wrote `db-config.json` (password in the secret store), and the daemon booted on Postgres with the same tasks and connections | new |
| 22 | `tests/security` route authorization | **PASS** (local and CI). The rewrite is 3e-iii-c's | same |
| 23 | `-tenancy multi` (bug 4757) | **PASS (bug 4757 fixed).** `-tenancy multi` on a fresh dir exits 1: "this build of grain is single-tenant only, and this daemon would run multi-tenant (-tenancy=multi was named on the command line)… Start it with -tenancy=single". A dir whose kept `tenancy` file says `multi`, started with the default `auto`, refuses the same way ("kept in …/tenancy"). `-tenancy single` then starts it and rewrites the file to `single`. `-tenancy -h` now says multi "was the shared deployment" and that this build refuses it. Re-checked at 33e88fca9 | **fixed** |
| 24 | **New (4d, 4e):** deployment-level GCP keys, boards, memory on Postgres | **PASS**. `grain gcp-key set ci-key --project p --account …` gives `gcp-key:ci-key` with minter credential `gcp-key-minter-ci-key`. `POST /api/boards` and `PUT /api/memory/documents` work. All three survive a restart, and the guard accepts the dir | new |

## Previous failures and observations

| Item | Status |
|---|---|
| Team UI and team routes (3e) | **UI fixed at 33e88fca9 (3e-ii, landed during this run). Routes still failing, expected.** 3e-iii-b … 3e-v haven't landed. `/api/teams*` (31 routes), `adminAccounts`/`isAdmin`/`teams` in `/api/config`, `tenancy.go`, `team_scope.go` and the `-tenancy` flag remain |
| Bug 4756: single-tenant daemon accepts `POST /api/teams`, then refuses its own dir | **Fixed** (PR #4176). 409 on both builds, SQLite and Postgres. The UI path showed the refusal in the dialog, and 3e-ii has since removed the item |
| Bug 4757: `-tenancy multi` starts, and its first team makes the dir unstartable | **Fixed** (PR #4177). Refuses named or kept `multi` |
| Check #2's bug A: `connections import` on a Postgres-backed dir | **Fixed** (PR #4185, task 4764) |
| Check #2's bug C: `pkg/relay` `t.Logf` race | **Fixed** (PR #4180, task 4765). `query_test_history`: 85 executions, 0 failures since |
| Default-model mismatch (`gemini-3.1-pro` in the seed and the `import -h` example) | **Still failing.** Unchanged (bug 1 of the 10-18 candidate qualification) |
| Schema-version message on Postgres ("move the store file out of the way") | **Fixed by stage 7.** Both stores now get the guard's message ("…needs a fresh data dir… Nothing in this data dir has been changed") |
| `/api/agent-models` reports agy's "Please sign in to view available models" | **Still present** |
| Team wording in logs ("a team admin of \"default\" adds one in Team settings -> Models & conn…"), connection names like `grain-team-ZGVmYXVsdA-c-…` | **Still present** (3e-iv/3e-v territory) |
| `GET /api/teams/default/git-connections` reports `status.host: "github.com"` for the anonymous `git.example.test` connection | **Still present** |
| Two daemons on one host collide on the default sandbox ports | **Still present** (not re-tried; every daemon here had its own `-sandbox-port-base`) |

## Full suites

| Suite | Result |
|---|---|
| grain CI, full workflow, **uncached** (`retrigger_ci` `*`, run **b097f54ae0823f77**, on this doc's placeholder at 452174231, whose tree is a1eef38dc's plus this file) | **All 11 jobs green.** `go` 12m25s (every package `ok`, including `pkg/relay` 19s, `tests/e2e` 442s, `tests/security` 591s, `pkg/ui` 473s, `pkg/orchestrator` 469s), `ui e2e` 22m18s, `frontend` 6m54s, `installer` 4m40s, `postgres`, `hub e2e`, `run host`, `package cache`, `relay image`, `lint`, `terraform` |
| Local `go test ./tests/e2e/` (no `-race`), a1eef38dc | 134 pass, 18 skip (live-agent, load, soak and stress tests), **0 fail**. Same count as last time |
| Local `go test ./tests/security/... ./google/...`, a1eef38dc | pass (9 packages `ok`) |
| This branch's full suite on 33e88fca9 plus this doc (run 6284e93c8db59532) | **All green except `go`**: one `-race` report in `pkg/ui` (bug E below). Every other package was `ok` (`tests/e2e` 435s, `tests/security` 527s). `go` was re-run uncached (`retrigger_ci`, run 3b3a8b695586107a); the result is in the pull request's checks |

**One red, a new flake from 3e-iii-a.** `pkg/ui`'s
`TestLocalScriptApprovalIsAnsweredByAPersonAndRecorded` raced under
`-race`, and the report failed the 19 other tests running in parallel with
it. `query_test_history`: its first failure in 146 executions. The same
test passed on a1eef38dc's uncached run and on 4777's own runs. It isn't
the `pkg/relay` race from check #2, which hasn't failed since 4765.

## Bugs

Fixed since the last check: **4756**, **4757**, check #2's **A** (4764) and
**C** (4765).

Still open: the default-model mismatch (bug 1 of the 10-18 candidate
qualification; not re-filed).

New this check (proposed as tasks by this run, not auto-merge, against `main`; filed unapproved when the run ends):

- **D. The pre-reset guard tells a single-tenant install it "holds
  multi-tenant data".** Stage 7's `CheckSchemaBaseline` refuses every
  version below the baseline with `ErrMultiTenantData`, so a pre-reset
  dir started `-tenancy single`, with no team or account, is told "this
  data dir holds multi-tenant data: schema 17…". `grain state check
  -migrate <state-repo>` gives the same text, which also says "this data
  dir" and "start this build on an empty one" about a state repository.
  Refusing is right. The wording should say that the store predates the
  single-tenant baseline, and leave out "multi-tenant" when the store
  holds no tenancy data. Repro: start a build of `3e5b6c386` with
  `-tenancy single` on an empty dir and stop it; start `main` on it, or
  run `grain state check -migrate <dir>/state-repo`. *Owner: stage 7
  (4686, landed). Could fold into stage 6 part two, which bumps the
  version again.*

- **E. `pkg/ui`: `SetAccessCodeForTest` races a server that is already
  serving.** 3e-iii-a's helper (`serverAccessClient` →
  `ui.SetAccessCodeForTest`, `pkg/ui/export_test.go:59`) writes
  `Config.LocalAPIToken` and `s.accessCode` unsynchronised.
  `TestLocalScriptApprovalIsAnsweredByAPersonAndRecorded` starts a
  goroutine that polls the server (`run.WaitLocalScriptApproval`) before
  calling it, so the write races `localAPITokenNow`
  (`accesscode.go:220`) in a request in flight. Test-only. *Owner: 3e-iii-a
  (4778, landed). 3e-iii-b will add many more callers of the helper, so
  the helper itself should be made safe.*

## Observations, not filed

- **`grain git-ambient` with no arguments writes.** With every flag
  defaulted it recorded an ambient github.com connection
  (`grain-team-…-c-3fef11800c5a`) beside the existing host-default PAT
  connection `bot`, so `grain git-connections` lists two host-default
  github.com connections. Before 3e-i the command took a team argument
  (`grain team git-ambient <team>`), so it couldn't be run bare by
  accident. `--if-none` exists for the installer. Whether two host-default
  connections on one host is a fault wasn't checked here; worth a look in
  3e-iv, which moves these commands off `/api/teams/default/...`.
- Help and wording residue for 3e-iv/3e-v: `grain export -h` mentions "team
  state repositories" and `grain import -h` mentions "running store
  migrations" (stage 7 removed migrations). `grain create` still has
  `-team`. `grain gcp-key -h` says `unknown gcp-key subcommand "-h"`. At
  33e88fca9 the Settings time-zone field still says "Browser default or
  team default".
- A refused `-tenancy multi` start on an empty path still creates
  `secrets/secrets.key`, `store/`, `state/` and `state-repo/` before
  refusing. That's harmless, because the next `-tenancy single` start uses
  them.
- `PUT /api/memory/documents` with no `text` answers 200 with `length: 0`,
  and the document doesn't appear in the list.

## Brief vs. tree

- The brief says to find in-flight stage tasks with `list_grain_tasks` and
  to comment on open bugs' tasks. This run had neither a task-listing nor a
  task-comment tool, the same as check #2. In-flight work was read from
  `git log origin/main` and `docs/single-tenant-reset.md`.
- The brief lists 4736 as "3e"; 4736 landed only 3e-i and split the rest
  into the 3e-ii … 3e-v chain. 3e-ii (4777) and 3e-iii-a (4778) have
  landed.
- Stage 7 (4686) landed before 3e and stage 6 part two, not after them.
  Part two will be the first schema bump past the baseline (20).
- Check #2's practical notes held: `-sandbox-port-base` multiples of 1024,
  `agy` out of `ghcr.io/bwsalmon/grain/grain:main` (`/opt/antigravity` is
  the binary itself; link it as `/usr/local/bin/agy`), and
  `NODE_EXTRA_CA_CERTS` for Playwright's download. Postgres needs
  `PGPASSWORD` or a `~/.pgpass`, or `createdb` prompts forever.
