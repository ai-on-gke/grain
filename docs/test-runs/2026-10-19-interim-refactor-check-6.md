# 2026-10-19: interim refactor check 6

Task 4816 (2026-10-19 23:40Z – 10-20 ~01:20Z). This is the sixth rolling
check of the single-tenant reset on `main`, run while the remaining stages
are still in progress. It uses the [fifth check](2026-10-19-interim-refactor-check-5.md)
(task 4810, `main` at e4977c21d) as its template and baseline. Task 4716
does the full qualification after the reset.

## Setup

| | |
|---|---|
| Commit | `main` at **d2115416e** (merge of task 4836) when the run started. **`main` moved twice mid-run**: to **66a303126** (merge of task 4834, 3e-iv-b2c-i), where every row b2c-i touches (1, 2, 4, 5–9, 12, 13, 15, 21, 24, 25, 26 and the residue) was re-run and the second uncached CI run is on it; then to **5c1bbb8ec** (task 4826, the Silo hub removal, and 4843, the `hub e2e` job deleted), where rows 1, 2, 4, 5, 9, 10, 12, 15, 21 and 26 were re-run on both builds (core/SQLite, Google/Postgres) and the final CI run is on it. Re-fetched before reporting: still 5c1bbb8ec |
| Builds | **Google**: `go build a wrapper distribution's `cmd/grain`. **Core**: `go build ./cmd/grain`. Both again on 66a303126, after `cd ui && npm ci && npm run build`, so they embed the frontend (used for the browser check) |
| Daemons | `grain daemon -data-dir <fresh> -sandbox-backend none -ui-addr 127.0.0.1:<port> -sandbox-port-base <multiple of 1024>`. Google and core on SQLite; Google and core on PostgreSQL 15 (Debian package, local; later starts find the database through `db-config.json`). Four per head |
| Pre-reset data | Written by a build of `3e5b6c386`, the commit before stage 1 (4680): one dir `-tenancy multi` (a team `eng` and a registered account `alice`), one dir `-tenancy single`, one multi-tenant Postgres database (team `eng`) |
| Baseline-19 data (new) | Written by a build of **e4977c21d** (check #5's head, schema 19), SQLite and Postgres: the seed's connections, a task, a memory document and `targetRepos`. This is the "earlier post-reset `main`" for the stage 6 part two check |
| Stubs | **forgecli**: a shell script on the Google daemon's `PATH` that logs its calls, prints a token for `auth token`, and answers `api … user` and `api … user/repos` with an HTTP header block and canned JSON (`walker`; `walk-org/service-a`, `walk-org/service-b`). **GitHub**: `pkg/github/githubsim`, inside the e2e harness. The github.com PAT in the seed file is fake (GitHub answers it 401). Nothing of grain's own was stubbed |
| Real | Gemini API (this task's key), `agy` 1.2.2 copied out of `ghcr.io/bwsalmon/grain/grain:main` (`/opt/antigravity/antigravity`), Chromium headless shell (Playwright, `ui/node_modules`) for the UI |
| Host | Task sandbox, Debian 12 |

## Changed since last check (e4977c21d → 5c1bbb8ec)

Merged to `main` (reset stages and bug fixes; the
`docs/capabilities*.md` and `docs/design/capabilities.md` docs from the
e1010c47 host's tasks 176–210 aren't part of the reset and aren't listed):

| Commit | Task | What |
|---|---|---|
| `85dbd23aa` | 4805 | **Stage 3e-iii-c4 landed**: `-tenancy`, `model/tenancy.go`, `daemon.TenancyPath`, the kept `<data-dir>/tenancy`, `GRAIN_ALLOW_MULTI_TENANT` are gone; an older unit still passing `-tenancy` gets it named among ignored flags; `grain init -tenancy` is accepted and ignored |
| `50dcce158` | 4805 | `team-config-repo.spec.js` deleted |
| `efedf89ab` | (bug G) | **Bug G fixed**: a pre-reset data dir is refused before a store is created in it |
| `e65a1d3da` | 4817 | **3e-iii-c5**: the UI trust options (`-ui-trusted-cidrs` and the rest) are gone |
| `5abd8de0d` | 4820 | **3e-iii-c6**: hostinit's always-skipped admin steps and wrapper-installer's `make_admin` |
| `49d76fe0a` | 4821 | **3e-iii-c7**: `GRAIN_TENANCY` out of the install-walkthrough templates |
| `00ff869d8`, `88719db89` | 4793 | **3e-iii-d**: team RBAC (`team_scope.go`, `ci_scope.go`, `system_admin.go`, the role checks); the hub's submitter check kept |
| `394615300`, `18c773c83` | 4814 | **3e-iv-b2a**: every `/api/teams*` route is gone; the deployment routes have handlers of their own |
| `9e432414d` | 4833 | **3e-iv-b2b**: the `ui/src` team helpers |
| `a919696a2` | 4780 | **3e-v**: the multi-tenant docs retired; the threat model rewritten for single-tenant grain |
| `a3d2a5442` | 4836 | the team UI walkthrough templates (`.grain/`) retired |
| `c51746913` | 4768 | **Stage 6-ii-a**: the `user` table dropped; **`SchemaVersion` 20** |
| `2bd293c7c` | 4834 | **3e-iv-b2c-i (mid-run, 66a303126)**: `ui.Client`'s team methods, `CopyCredentials` and `POST /api/secrets/copy`, the team and per-repo credential setters, the team-admin checks |
| `6f5d809e4`, `4ebb4e926`, `1d1877e0e`, `2b3d44701` | 4826 | **The Silo hub connection removed (mid-run, d2bdd3562)**, not a plan stage: the hub flags, routes (`GET /api/hub/status`, `POST /api/tasks/{id}/hub-proposals`), Silo claims in local policy, and the `hub_filing` and `task_silo_attestation` tables (out of the schema with no version bump; see observations). With it go the hub's `UserInTeam` checks of submitters and approvers, half of 3e-iv-b2c-ii |
| `4b7f3e289` | 4843 | **(mid-run, 5c1bbb8ec)** the `hub e2e` placeholder CI job deleted |

**Still in flight** (from `docs/single-tenant-reset.md` at 5c1bbb8ec and
the task branches on the remote; this run had no task-listing tool):
**3e-iv-b2c-ii** (`Store.IsApprover`'s `team:`/`team-admins:` tokens,
which still reach `UserInTeam` through `pkg/model/sensitive_path.go`; the
hub's own checks went with 4826), **3e-iv-b2c-iii**
(team membership itself: `Team.Members`/`Users`/`Admins`, `TeamRole`,
the `team_user` reads and writes, `teams.go`'s remainder) and **stage
6-ii-b** (`team`, `team_user`, `team_sequence`, `team_repo_config`,
`team_gcp_key`, the team columns, `hub_filing.team`).

## Results

| # | Flow | Result | vs. last check |
|---|---|---|---|
| 1 | Build both mains; boot on fresh dirs (SQLite: Google and core; Postgres: Google and core) | **PASS** on d2115416e, 66a303126 and 5c1bbb8ec. **A fresh dir no longer writes `tenancy`** (3e-iii-c4): it holds `cache`, `hub-audit.log`, `local-api-token`, `secrets`, `state`, `state-repo` and `store` (or `db-config.json` on Postgres). Google advertises `forgecli` in `/api/git-ambient-sources`; core advertises `[]` | improved (3e-iii-c4) |
| 2 | Access-code sign-in, no username/password | **PASS** (both builds, SQLite and Postgres, both heads). `/` without the code is 401; a wrong code is 401; the right code, lower-case with a space in it, is 303. With the code, `/api/auth/login`, `/register`, `/change-password` and `/api/users` are 404. `/login`, `/register` and `/google/login` are 404 from a build without the frontend and 200 (the SPA's `index.html`, like any unknown page) from one with it embedded; neither is a sign-in route | same |
| 3 | No team UI anywhere | **PASS in the browser** (d2115416e; b2c-i didn't touch `ui/src`). After sign-in, `/` has no "switch teams", "Team settings", "Team home", "Teams" or "new team" text; `/t/default/tasks` and `/t/eng/tasks` redirect to `/`; no page load calls `/api/teams*`. `/system/models` (→ `/admin/models`) shows `gemini-3.1-pro`; `/system/git` (→ `/admin/git`) shows `bot` and `git.example.test`. Text residue: see below | same |
| 4 | No leftover team/admin routes | **Mostly PASS, the rest expected (3e-iv-b2c-ii/iii)**. **`GET /api/teams` and `/api/teams/default…` are 404** (3e-iv-b2a), and `pkg/ui/team_scope.go`, `pkg/model/tenancy.go` and `sensitive_path.go` are gone. Still there: `/api/config` has `adminAccounts: ["debian"]`, `isAdmin: true` and `teams` (the default team, `members: [{user: debian, role: admin}]`), plus `tenancy: "single"`; `pkg/ui/teams.go` (2,099 lines at d2115416e) and `pkg/model/team_store.go`. `GET /api/deployment` still answers the default team's row (`name: "admin"`, `users`, `admins`, `members`): the handlers are its own now, but the body is unchanged. On 5c1bbb8ec, `/api/hub/status` and `/api/hub/filings` are 404 | improved (3e-iii-d, 3e-iv-b2a) |
| 5 | Guard: refuses a pre-reset multi-tenant dir | **PASS, bug G fixed.** Daemon start (both builds, SQLite and Postgres, both heads) exits 1: "this build of grain is single-tenant only, and this data dir was written by a build from before the single-tenant reset: schema 17, from before the single-tenant baseline (schema 19)…". **The Postgres dir started without `-postgres-url` now gets the same message, and `diff -r` is clean (no `store/grain.db`)**. With the URL too. `pg_dump` unchanged | fixed (G) |
| 6 | Guard: pre-reset *single-tenant* dir | **PASS.** Same message; dir unchanged | same |
| 7 | Guard: accepts a fresh dir | **PASS**. Restarts are clean on all eight daemons after imports, GCP keys, boards and memory documents | same |
| 8 | Guard: dirs the new build writes itself | **PASS**. With `/api/teams` gone there is no route left to write a second team (row 4); every restart of a dir `main` wrote is clean | changed (no team route to try) |
| 9 | `grain connections import` from a seed | **PASS on SQLite and Postgres**, both heads. `imported 2 git and 1 model connections`, twice. `secrets/github/bot.token` is 0600 | same |
| 10 | Gemini discovery (live) | **PASS**. 11 models (gemini-3.8/3.7/3.6-flash at high/medium/low, gemini-3.1-pro-high/low) on all eight daemons and on the imported dirs. **Default-model mismatch: still open** (the seed's `default_model: gemini-3.1-pro`, also `grain connections import -h`'s example): `the default agent framework is not ready: no configured agent backend matches team "default", model "gemini-3.1-pro": of 1 configured, 1 not serving that model -- connections are the team's own, so a team admin of "default" adds one in Team settings -> Models & connections…` | still failing |
| 11 | PAT / GitHub App against the fake GitHub | **PASS (e2e harness)**. `tests/e2e` (local and CI); `pkg/gitproxy` in CI | same |
| 12 | Command-backed forge via fake `forgecli`, wrapper distribution build | **PASS** (SQLite and Postgres, both heads). `POST /api/deployment/git-connections {host: git.example.com, type: ambient, source: forgecli}` is 200; it runs `forgecli auth token`, `forgecli api --include … user` and `… user/repos`; `ambientLogin: walker`; `GET /api/deployment/reachable-repos` lists `walk-org/service-a` and `service-b` | same |
| 13 | `forgecli` in the core build | **PASS**. 400 `unknown ambient source "forgecli": gh, git, auto` | same |
| 14 | Per-model pause | **PASS (CI)**. `pkg/orchestrator` and `pkg/cli` in the uncached `go` job | same |
| 15 | Task lifecycle: create (CLI) → approve | **PASS (live)**, both heads. `grain create -repo acme/widgets …` filed task 2; `grain approve 2` queued it. `grain create -team` still exists and fails `team "eng" does not exist` | same |
| 16 | Lifecycle end to end (sandbox clone, push, PR, grain CI, merge queue, merge) | **PASS (e2e harness)**. Local `tests/e2e`, plus `TestMergeQueue*` and `pkg/daemon` in CI | same |
| 17 | Create through MCP, including proposals | **PASS (e2e harness / CI)** | same |
| 18 | Create through the UI | **PASS (CI)**. `ui e2e` job, uncached, both heads | same |
| 19 | Schedules and templates firing | **PASS**. The e2e schedule walkthroughs pass; a fresh daemon seeds the system schedules | same |
| 20 | Director / interactive chat | **PASS**. A fresh daemon files the director console session (task 1, `awaiting_reply`) | same |
| 21 | State-repo export / import round trip | **PASS (live)**, both heads. `grain export -out` of core/SQLite ("exported 10 files … team state repos: 0"), `grain import` into a fresh dir, boot: tasks 1 and 2, all three git connections, the Gemini connection with its 11 models, the GCP key, the board, the memory document, `defaultModel` and `targetRepos`. The imported dir gets a new access code | same |
| 21a | `grain upgrade-postgres` on the baseline | **PASS**. The imported SQLite dir: **101 tables** moved in 2.0s (102 last time; `user` is gone at 20), `db-config.json` written, the daemon booted on Postgres with the same tasks | same |
| 22 | `tests/security` route authorization | **PASS** (local and CI) | same |
| 23 | `-tenancy multi` (bug 4757) | **Changed, as the plan says (3e-iii-c4).** `-tenancy` is no longer in `grain daemon -h`. `-tenancy multi` and `-tenancy single` on a fresh dir both start a single-tenant daemon and log "WARNING: starting anyway, ignoring 1 flag(s) this build does not define: -tenancy". A dir with a kept `tenancy` file, `single` or `multi`, starts single-tenant; the file is ignored and left on disk | changed (3e-iii-c4) |
| 24 | Deployment-level GCP keys, boards, memory | **PASS** on core/SQLite, both heads. `grain gcp-key set ci-key …` gives `gcp-key:ci-key`; `POST /api/boards` is 201; `PUT /api/memory/documents` is 200. All survive export/import and the move to Postgres | same |
| 25 | The deployment CLI and routes on `/api/deployment` | **PASS**, both heads. `grain git-connections`, `grain default-model gemini-3.1-pro-high`, `PUT /api/deployment {targetRepos: […]}` read back | same |
| 26 | **New: a baseline-19 store from an earlier post-reset `main`** (stage 6-ii-a, `SchemaVersion` 20) | **Refusal PASS; recovery FAIL (bug H).** Both builds, SQLite and Postgres, both heads, exit 1 with "applying schema: database schema is older than this build: found 19, this build knows 20 -- ../scripts/setup.sh moves such a store aside at deploy; to do it by hand, move the store file out of the way and let this build create a fresh one, and run `grain state reformat` alongside it…"; `diff -r` clean, `pg_dump` unchanged. **SQLite:** `grain state -data-dir D prepare-upgrade` ("rehearsed: 31 rows across 17 tables migrate from schema 19 into 20", "warning: tables/user.json names no table this build has") then start carries every row (task 2, connections, memory doc, `targetRepos`), but the refusal doesn't mention it. Following the refusal's own advice (move the store aside, `state reformat`, start) leaves only a new director task. **Postgres:** `prepare-upgrade` prints `none` (`ErrNoSQLiteStore`) and the next start is refused again, so it has no way forward. A `grain export` bundle from e4977c21d is refused by `main`'s `grain import` with the same "found 19" error; with only the store moved aside, the schema-19 state-repo dump is refused. The same on 5c1bbb8ec, where `prepare-upgrade` also warns that `hub_filing.json` and `task_silo_attestation.json` name no table this build has. A store at 20 written by 66a303126 starts on 5c1bbb8ec with its tasks (SQLite and Postgres) | new |

## Previous failures and observations

| Item | Status |
|---|---|
| Team routes (3e-iv-b2) | **Fixed** (3e-iv-b2a, task 4814): every `/api/teams*` route is 404 |
| `/api/config`'s `adminAccounts`/`isAdmin`/`teams` (3e-iii-d/3e-iv-b2) | **Still present**: `adminAccounts: ["debian"]`, `isAdmin: true`, `teams` (the default team with `members`), plus `tenancy: "single"`. Not in 3e-iii-d's list of `/api/config` fields; membership goes with 3e-iv-b2c-iii |
| What `GET /api/deployment` answers | **Unchanged body**: the default team's row, `name: "admin"`, `configRepo`, `users`, `admins`, `members: [{user: debian, role: admin}]`, `gitConnections`, `defaultModel`, `targetRepos`. The handlers are its own (3e-iv-b2a); `members`/`users`/`admins` go with b2c-iii |
| The `-tenancy` flag and the `tenancy` file (3e-iii-c4) | **Fixed as planned**: a fresh dir writes no `tenancy`; the flag is named as ignored; a kept file (`single` or `multi`) is ignored (row 23) |
| Bug G: a pre-reset Postgres dir started without `-postgres-url` | **Fixed** (`efedf89ab`). Row 5 |
| Bug F | Still fixed: `diff -r` is clean after every refused start in this run (pre-reset and baseline-19) |
| Default-model mismatch (`gemini-3.1-pro` in the seed and the `import -h` example) | **Still failing** (row 10) |
| `grain git-ambient` with no arguments writes a second host-default github.com connection | **Still present** (both heads): "This deployment now clones, pushes and calls GitHub as this host's own gh login on github.com", and `grain git-connections` lists `github.com bot` and `github.com grain-team-ZGVmYXVsdA-c-…` |
| `GET …/git-connections` reports `status.host: "github.com"` for the anonymous `git.example.test` connection | **Still present** |
| `/api/agent-models` reports agy's "Please sign in to view available models" | **Still present** |
| Help and wording residue | **Still present**: `grain export -h` "team state repositories"; `grain import -h` "running store migrations"; `grain create -team`; `grain gcp-key -h` → `unknown gcp-key subcommand "-h"`; Settings' "Browser default or team default"; the "not ready" log line's "a team admin of \"default\" adds one in Team settings -> Models & connections"; connection names `grain-team-ZGVmYXVsdA-c-…`; the Git connections tab (browser): "…other git hosts for this team.", "Which connection each of this team's repositories clones and pushes with…" and "This team names no target repositories yet. Add them on the Team tab to choose a connection for one." |
| `PUT /api/memory/documents` with no `text` answers 200 with `length: 0`, and the document doesn't appear in the list | **Still present**; the response's `summary` is also empty though one was sent |
| A refused `-tenancy multi` start on an empty path creates `secrets/`, `store/`, `state-repo/` | **Gone with the flag**: `-tenancy multi` is no longer refused (row 23) |
| Two daemons on one host collide on the default sandbox ports | Not re-tried; every daemon here had its own `-sandbox-port-base` |
| Stage 6 part two: a baseline-19 store from an earlier post-reset `main` | **Now applicable** (6-ii-a landed): refused correctly, recovery broken (row 26, bug H) |

## Full suites

| Suite | Result |
|---|---|
| grain CI, full workflow, **uncached**, on d2115416e (`retrigger_ci` `*`, run **958d44fba2870a76**, at 8ea37b953: d2115416e plus this doc's placeholder) | **All 11 jobs green.** `go` 11m30s, `ui e2e` 15m50s, `frontend` 5m45s, `installer` 4m29s, `run host` 2m39s, `package cache` 2m9s, `postgres` 2m5s, `hub e2e` 1m44s, `relay image` 1m9s, `terraform` 50s, `lint` 25s. No reds. (`wait_for_checks` started a second run, 71c153137c2929c7, on the same commit: also all green) |
| grain CI, full workflow, **uncached**, on 66a303126 (`retrigger_ci` `*`, run **c1ab6b8652aacb56**, at f36854987: 66a303126 merged into this branch) | **All 11 jobs green, one retry.** `go`'s first attempt failed in step "Test"; the automatic retry in a fresh sandbox passed (21m4s in all). Only the retry's log is kept, and `query_test_history` doesn't show the first attempt, so **the failing test isn't known**. It passed on d2115416e uncached, and on the retry, so it's counted as a flake, not a new red. `ui e2e` 15m43s, `frontend` 5m30s, `installer` 4m26s, `run host` 2m30s, `postgres` 2m5s, `package cache` 2m2s, `hub e2e` 1m40s, `relay image` 1m10s, `terraform` 47s, `lint` 27s |
| grain CI, full workflow, on 5c1bbb8ec (`wait_for_checks`, at b5842645e: 5c1bbb8ec merged into this branch, plus this report) | This report's own commit: its grain CI verdict is the pull request's gate, recorded in the task's closing note (the doc can't carry the verdict on itself) |
| Local `go test -count=1 ./tests/e2e/` (no `-race`), d2115416e | **127 pass, 16 skip (live-agent, load, soak and stress tests), 0 fail**, 247s. Same as last time |
| Local `go test -count=1 ./tests/security/... ./google/...`, d2115416e | **pass** (`tests/security` 49s; every `google/...` package `ok`, 8 packages) |

## Bugs

Fixed since the last check: **G** (`efedf89ab`).

Still open: the default-model mismatch (bug 1 of the 10-18 candidate
qualification). Not re-filed; this run had no task-comment tool, and there
is no new evidence beyond row 10.

New this check (proposed as a task by this run, not auto-merge, against
`main`):

- **H. Schema 19→20: the refusal advises a data-losing reset, and a
  Postgres store at 19 has no migration path.** `main` correctly refuses
  a store at 19 and leaves it untouched. But its message tells the
  operator to move the store aside and run `grain state reformat`, which
  starts empty. `grain state prepare-upgrade` is the step that carries the
  rows, and it works on SQLite, but the message doesn't name it. On
  Postgres, `prepare-upgrade` prints `none` (`ErrNoSQLiteStore`) and there
  is no way forward; `grain import` of an e4977c21d bundle and a start on
  the schema-19 state-repo dump are both refused. Repro in row 26. *Owner:
  stage 6 part two (6-ii-a, task 4768, landed; 6-ii-b's bump to 21 will
  take the same path).*

## Observations, not filed

- `GET /api/deployment` and `/api/config` still carry `members`, `users`,
  `admins` and `adminAccounts`/`isAdmin` (row 4); they go with
  3e-iv-b2c-iii.
- 3e-iii-c4's plan entry says the gitproxy/orchestrator/store
  `SingleTenant` switches "go with team RBAC in 3e-iii-d", but 3e-iii-d's
  entry doesn't list them, and they are still in the tree
  (`pkg/gitproxy/core.go`, `pkg/orchestrator/cycle.go` (`if
  !deps.SingleTenant && …`), `pkg/model/store.go`), always set to true by
  the daemon. Dead branches, not behaviour.
- The hub removal (4826) takes `hub_filing` and `task_silo_attestation`
  out of the schema without bumping `SchemaVersion` (still 20). A store at
  20 written before it keeps both tables (Postgres: listed in
  `pg_tables` after a start on 5c1bbb8ec), so two stores stamped 20 now
  hold different table sets, and their exports carry rows that an import
  warns "names no table this build has" about and skips. Nothing failed.
  The plan's stage 6 rule (each dropped table is a schema change after
  the baseline) wasn't applied; 6-ii-b's bump will settle it either way.
- A fresh dir on 5c1bbb8ec still writes `hub-audit.log`: the local policy
  audit log, which 4826 says stays.
- `/api/secrets/copy` (removed by b2c-i) answers 405 rather than 404,
  presumably because another `/api/secrets/{…}` pattern matches the path.

## Brief vs. tree

- The brief says to find in-flight stage tasks with `list_grain_tasks` and
  to comment on open bugs' tasks. This run had neither a task-listing nor
  a task-comment tool, the same as checks #2–#5. In-flight work was read
  from `git log origin/main`, `docs/single-tenant-reset.md` and the
  `grain/eu/task-*` branches on the remote (4834, 4826 and 4843 landed
  mid-run). The next check's brief, written before 4826 landed, lists it
  as in flight; it has landed.
- The brief's in-flight list has been overtaken: 3e-iii-c4, 3e-iii-d,
  3e-iv-b2a/b, 3e-v and 6-ii-a all landed. 3e-iv-b2c is split into i
  (landed), ii and iii, and stage 6 part two into 6-ii-a (landed) and
  6-ii-b.
- Report #5's practical notes held. Two more: a baseline-19 store needs
  `grain state -data-dir D prepare-upgrade` before `main` will start on it
  (SQLite only; bug H), and a binary built after `npm run build` serves
  the SPA's `index.html` (200) for any unknown non-API path, `/login`
  included. Playwright's Chromium download was needed again
  (`npx playwright install --with-deps chromium`).
