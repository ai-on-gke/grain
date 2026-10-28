# 2026-10-20: interim refactor check 7

Task 4852 (2026-10-20 04:09Z – ~05:00Z). This is the seventh rolling
check of the single-tenant reset on `main`, run while the remaining stages
are still in progress. It uses the [sixth check](2026-10-19-interim-refactor-check-6.md)
(task 4816, `main` at 66a303126 / 5c1bbb8ec) as its template and baseline.
Task 4716 does the full qualification after the reset.

## Setup

| | |
|---|---|
| Commit | `main` at **123c390a2** (merge of task 4839, stage 6-ii-b1) when the run started. Re-fetched before reporting: still 123c390a2 |
| Builds | **Google**: `go build ./google/cmd/grain`. **Core**: `go build ./cmd/grain`. Core again after `cd ui && npm ci && npm run build`, so it embeds the frontend (used for the browser check) |
| Daemons | `grain daemon -data-dir <fresh> -sandbox-backend none -ui-addr 127.0.0.1:<port> -sandbox-port-base <multiple of 1024>`. Google and core on SQLite; Google and core on PostgreSQL 15 (Debian package, local; later starts find the database through `db-config.json`) |
| Pre-reset data | Written by a build of `3e5b6c386`, the commit before stage 1 (4680): one dir `-tenancy multi` (a team `eng` and a registered account `alice`), one dir `-tenancy single` (a team `eng`; registration is 404 there), one multi-tenant Postgres database (team `eng`, `alice`) |
| Baseline-19 data | Written by a build of **e4977c21d** (check #5's head, schema 19), SQLite and Postgres: the seed's connections, a task, a memory document and `targetRepos`, plus a `grain export` bundle of each |
| Baseline-20 data (new) | The same, written by a build of **66a303126** (check #6's head, schema 20) |
| Stubs | **ggh**: a shell script on the Google daemon's `PATH` that logs its calls, prints a token for `auth token`, and answers `api … user` and `api … user/repos` with an HTTP header block and canned JSON (`walker`; `walk-org/service-a`, `walk-org/service-b`). **GitHub**: `pkg/github/githubsim`, inside the e2e harness. The github.com PAT in the seed file is fake (GitHub answers it 401). Nothing of grain's own was stubbed |
| Real | Gemini API (this task's key), `agy` 1.2.2 copied out of `ghcr.io/bwsalmon/grain/grain:main` (`/opt/antigravity/antigravity`), Chromium headless shell (Playwright, `ui/node_modules`) for the UI |
| Host | Task sandbox, Debian 12 |

## Changed since last check (66a303126 → 123c390a2)

Merged to `main` (reset stages and bug fixes; the
`docs/capabilities*.md` and Grainfile spec docs from the e1010c47 host's
tasks 159–242 aren't part of the reset and aren't listed):

| Commit | Task | What |
|---|---|---|
| `6f5d809e4`, `4ebb4e926`, `1d1877e0e`, `2b3d44701` | 4826 | **The Silo hub connection removed** (already re-checked mid-run by #6 at 5c1bbb8ec): hub flags, routes, `hub_filing` and `task_silo_attestation` |
| `4b7f3e289` | 4843 | the `hub e2e` CI job deleted |
| `40b030f7d`, `10b28ffed`, `3febf8ead` | 4840 (4834) | **3e-iv-b2c-ii**: hub submitters/approvers and `Store.IsApprover`'s `team:`/`team-admins:` tokens are decided on the deployment's admin accounts. With the hub gone (4826), only `IsApprover` is left of it on `main` |
| `5cc2e25ab`, `f5d700eae` | 4844 (4826) | **`Task.Team` removed**: the field, `POST /api/tasks/{id}/move`, `grain create -team`, `grain edit -team`, the UI's team pickers. The `task.team` column stays for 6-ii-b5 |
| `dcae2d806` | 4851 | **Bug H fixed**: the `ErrSchemaTooOld` refusal names `grain state prepare-upgrade`, and `prepare-upgrade` and the start-time migration work on Postgres (`postgres.ArchiveStore`) |
| `71f20dfe0`, `8fecd6633` | 4853 (4851) | `grain import` of a bundle exported one schema back writes and rehearses the upgrade note; the next start migrates it |
| `7b0511443`, `3ffe3afce` | 4841 (4834) | **3e-iv-b2c-iii**: team membership removed from the model and the store (`Team.Members`/`Users`/`Admins`, `TeamMember`, `TeamRole`, `UserInTeam`/`UserTeams`, the `team_user` reads and writes, the IAP reconciler's team users). **Stage 3 is landed** |
| `fc40434b2` | 4839 (4768) | **Stage 6-ii-b1**: `team_user` and `team_sequence` dropped; **`SchemaVersion` 21**. The plan splits the rest of 6-ii-b into b2–b5 |

**Still in flight** (from `docs/single-tenant-reset.md` at 123c390a2 and
the task branches on the remote; this run had no task-listing tool):
**6-ii-b2** (task 4861, branch open: the default team's git connections,
default model and model settings move to `grain_config`, schema 22),
**6-ii-b3** (`team_repo_config`, `team_gcp_key`; not started), **6-ii-b4**
(`team`; not started) and **6-ii-b5** (the team columns; not started).

## Results

| # | Flow | Result | vs. last check |
|---|---|---|---|
| 1 | Build both mains; boot on fresh dirs (SQLite: Google and core; Postgres: Google and core) | **PASS**. A fresh dir holds `cache`, `hub-audit.log`, `local-api-token`, `secrets`, `state`, `state-repo` and `store` (or `db-config.json` on Postgres). Google advertises `ggh` in `/api/git-ambient-sources`; core advertises `[]` | same |
| 2 | Access-code sign-in, no username/password | **PASS** (both builds, SQLite and Postgres). `/` without the code is 401; a wrong code is 401; the right code, lower-case with a space in it, is 303. With the code, `/api/auth/login`, `/register`, `/change-password`, `/api/users`, `/api/teams`, `/api/teams/default` and `/api/hub/status` are 404; `POST /api/tasks/1/move` is 404 (4844). `/login` is 404 from a build without the frontend and 200 (the SPA's `index.html`) from one with it | same |
| 3 | No team UI anywhere | **PASS in the browser**. After sign-in, `/` has no "switch teams", "Team settings", "Team home", "Teams" or "new team" text; `/t/default/tasks` and `/t/eng/tasks` redirect to `/`; no page load calls `/api/teams*`. `/system/models` (→ `/admin/models`) shows `gemini-3.1-pro`; `/system/git` (→ `/admin/git`) shows `bot` and `git.example.test`. Text residue: see below | same |
| 4 | `/api/config` and `GET /api/deployment` | **Changed as planned (3e-iv-b2c-iii).** `/api/deployment` no longer has `members`, `users` or `admins`: `id`, `name: "admin"`, `description`, `configRepo`, `configCloneUrl`, `cloneCommand`, `mergeBatchSize`, `defaultModel`, `gitConnections`, `hasGitCredential`, `targetRepos` (when set), `createdAt`. `/api/config`'s `teams` is still a one-element list with the same fields (no member fields); `adminAccounts: ["debian"]`, `isAdmin: true` and `tenancy: "single"` are still there | improved (b2c-iii) |
| 5 | Guard: refuses a pre-reset multi-tenant dir | **PASS** (both builds, SQLite, Postgres with and without `-postgres-url`). Exit 1: "this build of grain is single-tenant only, and this data dir was written by a build from before the single-tenant reset: schema 17, from before the single-tenant baseline (schema 19)…". `diff -r` clean; `pg_dump` unchanged | same |
| 6 | Guard: pre-reset *single-tenant* dir | **PASS.** Same message; dir unchanged | same |
| 7 | Guard: accepts a fresh dir | **PASS**. Restarts are clean on every daemon after imports, GCP keys, boards and memory documents | same |
| 8 | Guard: dirs the new build writes itself | **PASS**. Every restart of a dir `main` wrote is clean; there is no route left that writes a second team | same |
| 9 | `grain connections import` from a seed | **PASS on SQLite and Postgres**, both builds. `imported 2 git and 1 model connections`, twice. `secrets/github/bot.token` is 0600 | same |
| 10 | Gemini discovery (live) | **PASS**. 11 models (gemini-3.8/3.7/3.6-flash at high/medium/low, gemini-3.1-pro-high/low) on all four daemons, on the imported dir and on every upgraded store. **Default-model mismatch: still open** (the seed's `default_model: gemini-3.1-pro`, also `grain connections import -h`'s example): `the default agent framework is not ready: no configured agent backend matches team "default", model "gemini-3.1-pro": of 1 configured, 1 not serving that model -- connections are the team's own, so a team admin of "default" adds one in Team settings -> Models & connections…` | still failing |
| 11 | PAT / GitHub App against the fake GitHub | **PASS (e2e harness)**. `tests/e2e` (local and CI); `pkg/gitproxy` in CI | same |
| 12 | Command-backed forge via fake `ggh`, Google build | **PASS** (SQLite and Postgres). `POST /api/deployment/git-connections {host: depot.code.corp.goog, type: ambient, source: ggh}` is 200; it runs `ggh auth token`, `ggh api --include … user` and `… user/repos`; `GET /api/deployment/reachable-repos` lists `walk-org/service-a` and `service-b` | same |
| 13 | `ggh` in the core build | **PASS**. 400 `unknown ambient source "ggh": gh, git, auto` | same |
| 14 | Per-model pause | **PASS (CI)**. `pkg/orchestrator` and `pkg/cli` in the uncached `go` job | same |
| 15 | Task lifecycle: create (CLI) → approve | **PASS (live)**, core/SQLite and Google/Postgres. `grain create -repo acme/widgets …` filed task 2; `grain approve 2` queued it; the task's JSON has no `team`. **`grain create -team` is gone** (4844); `create -h` and `edit -h` name no `-team` | changed (4844) |
| 16 | Lifecycle end to end (sandbox clone, push, PR, grain CI, merge queue, merge) | **PASS (e2e harness)**. Local `tests/e2e`, plus `TestMergeQueue*` and `pkg/daemon` in CI | same |
| 17 | Create through MCP, including proposals | **PASS (e2e harness / CI)** | same |
| 18 | Create through the UI | **PASS (CI)**. `ui e2e` job | same |
| 19 | Schedules and templates firing | **PASS**. The e2e schedule walkthroughs pass; a fresh daemon seeds the system schedules | same |
| 20 | Director / interactive chat | **PASS**. A fresh daemon files the director console session (task 1) | same |
| 21 | State-repo export / import round trip | **PASS (live)**. `grain export -data-dir … -out` of core/SQLite ("exported 10 files … team state repos: 0"), `grain import` into a fresh dir, boot: tasks 1 and 2, all three git connections, the Gemini connection with its 11 models, the GCP key, the board, the memory document, `defaultModel`. The imported dir gets a new access code | same |
| 21a | `grain upgrade-postgres` on the baseline | **PASS**. The imported SQLite dir: **97 tables** moved in 1.9s (101 last time: `hub_filing`, `task_silo_attestation`, `team_user` and `team_sequence` are gone), `db-config.json` written, the daemon booted on Postgres with the same tasks. `team`, `team_gcp_key` and `team_repo_config` are still there (6-ii-b2–b4) | changed (4826, 6-ii-b1) |
| 22 | `tests/security` route authorization | **PASS** (local and CI) | same |
| 23 | `-tenancy multi` | **PASS, unchanged**: not in `grain daemon -h`; a fresh dir with `-tenancy multi` starts single-tenant and logs "WARNING: starting anyway, ignoring 1 flag(s) this build does not define: -tenancy" | same |
| 24 | Deployment-level GCP keys, boards, memory | **PASS** on core/SQLite and Google/Postgres. `grain gcp-key set ci-key --project p --account … FILE` gives `gcp-key:ci-key`; `POST /api/boards` is 201; `PUT /api/memory/documents` is 200. All survive export/import and the move to Postgres | same |
| 25 | The deployment CLI and routes on `/api/deployment` | **PASS**. `grain git-connections`, `grain default-model gemini-3.1-pro-high` ("This deployment now runs on gemini-3.1-pro-high by default."), `PUT /api/deployment {targetRepos: […]}` read back | same |
| 26 | **Schema upgrade to 21: a store at 19 (e4977c21d) and at 20 (66a303126)** | **PASS, bug H fixed.** Both builds, SQLite and Postgres, all four stores: the start exits 1 with "applying schema: database schema is older than this build: found 19 [20], this build knows 21 -- nothing has been changed. To carry this deployment's rows across, stop grain and run `grain state -data-dir DIR prepare-upgrade` with this build…, then start it again… Starting empty instead loses every task, connection and memory document: only if that is what you want, move the store aside yourself and run `grain state reformat`". `diff -r` clean; `pg_dump` unchanged. **`prepare-upgrade` works on SQLite and Postgres** ("rehearsed: 30 rows across 16 tables migrate from schema 19 [20] into 21", with warnings for `user`/`hub_filing`/`task_silo_attestation`/`team_user`/`team_sequence` naming no table). The next start logs "imported this deployment's rows … into a fresh store at schema 21" and "the config repository's dump was rewritten at schema 21", keeps `store.schema19-<time>` (SQLite) or a `public_schema19_<time>` schema (Postgres), and carries the task, the connections, the Gemini connection (11 models), the memory document, `defaultModel` and `targetRepos`. A second start is clean. **Bundles (4853):** `grain import` of an e4977c21d or 66a303126 *SQLite* bundle says "schema version: 19 [20], migrates to 21 when grain next starts on DIR", and the next start carries task 2. A *Postgres* bundle (which carries `db-config.json`, not the rows): `grain import` prints "schema version: **21**" and leaves a stray empty SQLite `store/grain.db`, while the database it points at is at 19. The next start refuses it correctly, and `prepare-upgrade` then carries the rows (bug I) | fixed (H); new bug I |

## Previous failures and observations

| Item | Status |
|---|---|
| Bug H: the schema-too-old refusal advises a data-losing reset; no Postgres path | **Fixed** (`dcae2d806`, 4851; bundles 71f20dfe0, 4853). Row 26 |
| `/api/config`'s `adminAccounts`/`isAdmin`/`teams` | **Partly changed**: `teams` lost its member fields; it's still a list holding the default team's row (that goes with 6-ii-b4). `adminAccounts` and `isAdmin` remain. On single-tenant `main` they now describe the deployment's admin accounts, which `IsApprover` and `requireRepoAdmin` use, so this isn't residue. `tenancy: "single"` remains |
| What `GET /api/deployment` answers | **Fixed (3e-iv-b2c-iii)**: no `members`, `users` or `admins`. It's still the default team's row (`name: "admin"`, `description: "Built-in admin team"`, `configRepo`), which 6-ii-b2/b4 move |
| The hub's submitter/approver checks (3e-iv-b2c-ii) | **Gone with the hub** (4826); `IsApprover`'s `team-admins:` token reads the admin accounts (40b030f7d, covered by `pkg/ui` approvals and `pkg/orchestrator` tests in CI). Nothing outside `pkg/model` mentions `UserInTeam` |
| Bugs F and G | Still fixed: `diff -r` is clean after every refused start (pre-reset, baseline-19, baseline-20), including the pre-reset Postgres dir started without `-postgres-url` |
| Default-model mismatch (`gemini-3.1-pro` in the seed and the `connections import -h` example) | **Still failing** (row 10) |
| `grain git-ambient` with no arguments writes a second host-default github.com connection | **Still present**: "This deployment now clones, pushes and calls GitHub as this host's own gh login on github.com", and `grain git-connections` lists `github.com bot` and `github.com grain-team-ZGVmYXVsdA-c-…` |
| `GET …/git-connections` reports `status.host: "github.com"` for the anonymous `git.example.test` connection | **Still present** |
| `/api/agent-models` reports agy's "Please sign in to view available models" | **Still present** |
| Help and wording residue | **One fixed, rest still present.** **Fixed:** `grain create -team` (gone with 4844). **Still present:** `grain export -h` "team state repositories"; `grain import -h` "running store migrations"; `grain gcp-key -h` → `unknown gcp-key subcommand "-h"`; Settings' "Browser default or team default" (`SettingsOverlay.jsx`) and the task detail's "(team default)" (`Declared.jsx`); the "not ready" log line's "a team admin of \"default\" adds one in Team settings -> Models & connections" (`pkg/daemon/agents.go`); connection names `grain-team-ZGVmYXVsdA-c-…`; the Git connections tab (browser): "…for this team.", "…this team's repositories…" and "This team names no target repositories yet. Add them on the Team tab…" (`TeamGitPanel.jsx`) |
| `PUT /api/memory/documents` with no `text` answers 200 with `length: 0` and an empty `summary` | **Still present** |
| Two stores stamped 20 with different table sets (4826 dropped two tables without a bump) | **Settled by 6-ii-b1's bump**: a store at 20 from 66a303126 (with `hub_filing`/`task_silo_attestation`) migrates to 21 like any other; the two tables' rows are passed over with a warning |
| `/api/secrets/copy` answers 405 rather than 404 | **Still present** |

## Full suites

| Suite | Result |
|---|---|
| grain CI, full workflow, **uncached**, on 123c390a2 (`retrigger_ci` `*`, run **3e22d0c50a78de06**, at e34241257: 123c390a2 plus this doc's placeholder) | **All 10 jobs green, one retry.** `go`'s first attempt failed in step "Test" again (as on 66a303126 in check #6); the automatic retry in a fresh sandbox passed (19m38s in all). Only the retry's log is kept, so **the failing test isn't known**. It has now happened on two heads and passed on retry both times, so it's counted as the known first-attempt flake, not a new red. `ui e2e` 15m45s, `frontend` 5m15s, `installer` 4m18s, `run host` 2m28s, `postgres` 2m4s, `package cache` 2m0s, `relay image` 1m9s, `terraform` 51s, `lint` 27s. (`wait_for_checks` answered from cached verdicts as run f5e0e3cc56add422 on the same commit: all green) |
| grain CI, full workflow, on this report's commit (`wait_for_checks`) | This report's own commit: its grain CI verdict is the pull request's gate, recorded in the task's closing note |
| Local `go test -count=1 ./tests/e2e/` (no `-race`), 123c390a2 | **123 pass, 14 skip (live-agent, load, soak and stress tests), 0 fail**, 223s. Fewer tests than last time (127/16) because 4826 deleted the hub round-trip tests |
| Local `go test -count=1 ./tests/security/... ./google/...`, 123c390a2 | **pass** (`tests/security` 47s; every `google/...` package `ok`, 8 packages) |

## Bugs

Fixed since the last check: **H** (`dcae2d806`, `71f20dfe0`).

Still open: the default-model mismatch (bug 1 of the 10-18 candidate
qualification). Not re-filed; this run had no task-comment tool, and there
is no new evidence beyond row 10.

New this check (proposed as a task by this run, not auto-merge, against
`main`):

- **I. `grain import` of a Postgres bundle one schema back reports
  "schema version: 21" and leaves a stray SQLite store.** A Postgres
  deployment's bundle carries `db-config.json`, not the rows. Imported by
  `main` while its database is still at 19, `grain import` prints
  "schema version: 21" and creates an empty SQLite `store/grain.db`,
  because `daemon.OpenStore("", "", dir)` in `pkg/cli/import.go` opens the
  embedded SQLite store and doesn't follow `db-config.json`. 71f20dfe0
  says such a dir "is refused with a message naming prepare-upgrade". The
  next start refuses correctly, and `prepare-upgrade` then carries every
  row, so nothing is lost. Repro in row 26. *Owner: follow-up to 4853
  (stage 6-ii-b's bundle route); every later bump takes this path.*

## Observations, not filed

- `/api/config` still carries `tenancy: "single"`, a constant on a build
  with no other mode.
- The 3e-iii-c4 `SingleTenant` switches noted last time
  (`pkg/gitproxy/core.go`, `pkg/orchestrator/cycle.go`,
  `pkg/model/store.go`) weren't re-read this run.
- A fresh dir still writes `hub-audit.log` (the local policy audit log,
  which 4826 says stays).
- A dir written by e4977c21d keeps its `tenancy` file through the 19→21
  upgrade; it's ignored, as row 23 of check #6 found.

## Brief vs. tree

- The brief says to find in-flight stage tasks with `list_grain_tasks` and
  to comment on open bugs' tasks. This run had neither a task-listing nor
  a task-comment tool, the same as checks #2–#6. In-flight work was read
  from `git log origin/main`, `docs/single-tenant-reset.md` and the
  `grain/eu/task-*` branches on the remote.
- The brief's in-flight list has been overtaken: 3e-iv-b2c-ii (4840),
  3e-iv-b2c-iii (4841), bug H (4851, 4853) and 4826 all landed, and
  stage 6-ii-b is split into b1 (4839, landed) and b2–b5. The hub's
  `UserInTeam` checks went with the hub (4826) before b2c-ii landed.
- Report #6's practical notes held, with corrections: `grain create`
  takes `-body` (not `-prompt`); `grain gcp-key set` needs `--project` and
  `--account`; `grain upgrade-postgres` takes `-dsn` (not
  `-postgres-url`); `grain export` works offline with `-data-dir`; the
  Postgres schema stamp is the `grain_schema` table. A Postgres bundle
  points at the *live* database, so upgrading the source database in
  place before importing its bundle confounds the test: restore the
  database from its `pg_dump` first (done here).
