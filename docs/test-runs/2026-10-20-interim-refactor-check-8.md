# 2026-10-20: interim refactor check 8

Task 4869 (2026-10-20 15:06Z – ~16:15Z). This is the eighth rolling
check of the single-tenant reset on `main`, run while the remaining stages
are still in progress. It uses the [seventh check](2026-10-20-interim-refactor-check-7.md)
(task 4852, `main` at 123c390a2, schema 21) as its template and baseline.
Task 4716 does the full qualification after the reset.

## Setup

| | |
|---|---|
| Commit | `main` at **bbc84069c** (schema 23) when the run started. **6-ii-b4 landed mid-run**: re-fetched at ~15:27Z, `main` was **79fb0647b** (schema 24; 4863, 4903, 4908, 4909, 4910 merged). Every row was re-run on 79fb0647b, and the results below are 79fb0647b's unless a row says otherwise. Re-fetched before reporting: **df1fc63ac**, which adds only 4907 (`fix(tests/container)`: the container schema-upgrade harness adds its git connection on the default host; no row here touches it) |
| Builds | **Google**: `go build a wrapper distribution's `cmd/grain`. **Core**: `go build ./cmd/grain`. Core again after `cd ui && npm ci && npm run build`, so it embeds the frontend (used for the browser check; rebuilt at 79fb0647b because 4908/4909 changed the UI) |
| Daemons | `grain daemon -data-dir <fresh> -sandbox-backend none -ui-addr 127.0.0.1:<port> -sandbox-port-base <multiple of 1024>`. Google and core on SQLite; Google and core on PostgreSQL 15 (Debian package, local; later starts find the database through `db-config.json`) |
| Pre-reset data | Written by a build of `3e5b6c386`, the commit before stage 1 (4680): one dir `-tenancy multi` (a team `eng` and a registered account `alice`), one dir `-tenancy single` (a team `eng`; registration is 404 there), one multi-tenant Postgres database (team `eng`, `alice`) |
| Baseline data | One SQLite dir and one Postgres database per schema, each written by a build of the commit that introduced that schema: **19** e4977c21d, **20** 66a303126, **21** 123c390a2, **22** e1efc3e01 (merge of 4861, 6-ii-b2; new), **23** bbc84069c (this run's starting head; new). Each holds the seed's connections, a task, a memory document, `targetRepos`, **a repo config on `acme/widgets`** (`setupCommand`, `promptExtension`, `defaultCapabilities`; on these builds it's the team layer, `team_repo_config`) and **a GCP key** (`team_gcp_key`), plus a `grain export` bundle and a `pg_dump` of each. The repo config and GCP key are new this check, to exercise 6-ii-b3's fold |
| Stubs | **forgecli**: a shell script on the Google daemon's `PATH` that logs its calls, prints a token for `auth token`, and answers `api … user` and `api … user/repos` with an HTTP header block and canned JSON (`walker`; `walk-org/service-a`, `walk-org/service-b`). **GitHub**: `pkg/github/githubsim`, inside the e2e harness. The github.com PAT in the seed file is fake (GitHub answers it 401). Nothing of grain's own was stubbed |
| Real | Gemini API (this task's key), `agy` copied out of `ghcr.io/bwsalmon/grain/grain:main` (`/opt/antigravity/antigravity`), Chromium headless shell (Playwright, `ui/node_modules`) for the UI |
| Host | Task sandbox, Debian 12 |

## Changed since last check (123c390a2 → 79fb0647b)

Merged to `main` (reset stages and bug fixes). Not listed, because they
aren't part of the reset: the Settings/System/Processes UI rework (4881,
4882, 4883, 4884, 4894–4899, 4905, 4909, 4910), which did remove most of
the team wording residue; the e1010c47 host's capability-contract change
(244); CI job coalescing (243); and the google/ design docs (94548571
tasks 3 and 4).

| Commit | Task | What |
|---|---|---|
| `7fe482be4` | 4870 | **Bug I fixed**: `grain import` of a Postgres bundle resolves the database the way the daemon does, reads its stamp without initialising it, and refuses a database a schema back with a message naming `prepare-upgrade`. It no longer creates `store/` |
| `40dcd9a78` | 4861 | **6-ii-b2**: the deployment's git connections, git credential, pinned connection, model settings and default model move from `team` to `grain_config`; **`SchemaVersion` 22**; dump fixup keyed 21 |
| `30ee81688`, `9a10adda2` | 4862 | **6-ii-b3**: `team_repo_config` folded into `repo_config` (which gains `git_credential`), `team_gcp_key` becomes a deployment `gcp_key` table, `DELETE /api/deployment/repos/{o}/{n}/config` gone; **`SchemaVersion` 23**; dump fixup keyed 22 |
| `ee87a3b42` (merge `3a5974e09`, mid-run) | 4863 | **6-ii-b4**: the `team` table dropped; `agent_framework`, `agent_git_name`/`email`, `target_repos`, `repair_template_id`, `merge_batch_size` on `grain_config`; `model.Team` and `team_store.go` gone; the guard's second-team count moves to `retiredMultiTenantChecks`; **`SchemaVersion` 24**; dump fixup keyed 23 |
| `5e2bf88d2` (mid-run) | 4908 | UI: the team leftovers dropped from sessions, schedules, templates and suites |
| `94d1fb8da`, `76d2efd42` (mid-run) | 4903 | `setup.sh` installs grain 2.0 under `/var/lib/grain/{data,scratch}` (not exercised here; `scripts/setup.sh` isn't in this check's flow table) |

**Still in flight** (from `docs/single-tenant-reset.md` at 79fb0647b and
the task branches on the remote; this run had no task-listing tool):
**6-ii-b5** (the team columns: `board.team`, `ci_run.team`,
`custom_field.team`, `executor_token.team`, `memory*.team`,
`schedule.team`, `suite*.team`, `task.task_team`, `template.team`,
`hub_filing.team`, the audit tables' `team_id`). The plan says "not
landed", and the remote has no branch for it yet.

## Results

| # | Flow | Result | vs. last check |
|---|---|---|---|
| 1 | Build both mains; boot on fresh dirs (SQLite: Google and core; Postgres: Google and core) | **PASS**. A fresh dir holds `cache`, `hub-audit.log`, `local-api-token`, `secrets`, `state`, `state-repo` and `store` (or `db-config.json` on Postgres). Google advertises `forgecli` in `/api/git-ambient-sources`; core advertises `[]` | same |
| 2 | Access-code sign-in, no username/password | **PASS** (both builds, SQLite and Postgres). `/` without the code is 401; a wrong code is 401; the right code, lower-case with a space in it, is 303. With the code, `/api/auth/login`, `/register`, `/change-password`, `/api/users`, `/api/teams`, `/api/teams/default`, `/api/teams/default/repos/acme/widgets/config` and `/api/hub/status` are 404; `POST /api/tasks/1/move` is 404. `DELETE /api/deployment/repos/acme/widgets/config` is now 405 (route gone with 6-ii-b3). `/login` is 404 from a build without the frontend and 200 (the SPA's `index.html`) from one with it | same (+ b3's DELETE gone) |
| 3 | No team UI anywhere | **PASS in the browser** (bbc84069c and 79fb0647b). After sign-in, `/`, `/settings/general`, `/settings/tasks`, `/settings/memory`, `/settings/repositories`, `/settings/models` and `/settings/git` have no "switch teams", "Team settings", "Team home", "Teams", "new team", "this team", "team default" or "Team tab" text. `/t/default/tasks` and `/t/eng/tasks` redirect to `/`; no page load calls `/api/teams*`. `/system/models` → `/settings/models` shows `gemini-3.1-pro`; `/system/git` → `/settings/git` shows `bot` and `git.example.test`. Text residue left: the task detail and New task (see below) | improved (UI rework) |
| 4 | `/api/config` and `GET /api/deployment` | **Changed as planned (6-ii-b2/b3/b4).** `/api/deployment` is now built from `grain_config`: `id: "default"`, `name: "admin"`, `configRepo`, `configCloneUrl`, `cloneCommand`, `mergeBatchSize` (a fresh dir now says **3**, `grain_config`'s default, where the team row said 0), `hasGitCredential`, and once set `defaultModel`, `gitConnections`, `targetRepos`, `repos`, `associatedRepos`. **`description` and `createdAt` are gone** (b4 dropped them). `/api/config` drops `teamRepoDefaultCapabilities` (b3). Its `teams` is **still a one-element list**, the same shape as `/api/deployment`, now built from `grain_config` (b4 keeps "the team shape the frontend reads"). `adminAccounts: ["debian"]`, `isAdmin: true` and `tenancy: "single"` are still there | changed (b2–b4) |
| 5 | Guard: refuses a pre-reset multi-tenant dir | **PASS** (both builds, SQLite, Postgres with and without `-postgres-url`), on bbc84069c and 79fb0647b. Exit 1: "this build of grain is single-tenant only, and this data dir was written by a build from before the single-tenant reset: schema 17, from before the single-tenant baseline (schema 19)…". `diff -r` clean; `pg_dump` unchanged | same |
| 6 | Guard: pre-reset *single-tenant* dir | **PASS.** Same message; dir unchanged | same |
| 7 | Guard: accepts a fresh dir | **PASS**. Restarts are clean on every daemon after imports, GCP keys, boards and memory documents | same |
| 8 | Guard: dirs the new build writes itself | **PASS**. Every restart of a dir `main` wrote is clean. With `team` gone (b4), a second team can only arrive in a dump, which `retiredMultiTenantChecks` refuses (CI: `TestADumpWithASecondTeamIsRefused`) | same |
| 9 | `grain connections import` from a seed | **PASS on SQLite and Postgres**, both builds. `imported 2 git and 1 model connections`, twice. `secrets/github/bot.token` is 0600 | same |
| 10 | Gemini discovery (live) | **PASS**. 11 models (gemini-3.8/3.7/3.6-flash at high/medium/low, gemini-3.1-pro-high/low) on all four fresh daemons, on the imported dirs and on all twenty upgraded stores. **Default-model mismatch: still open** (the seed's `default_model: gemini-3.1-pro`, also `grain connections import -h`'s example): `the default agent framework is not ready: no configured agent backend matches team "default", model "gemini-3.1-pro": of 1 configured, 1 not serving that model -- connections are the team's own, so a team admin of "default" adds one in Team settings -> Models & connections…` | still failing |
| 11 | PAT / GitHub App against the fake GitHub | **PASS (e2e harness)**. `tests/e2e` (local and CI); `pkg/gitproxy` in CI | same |
| 12 | Command-backed forge via fake `forgecli`, wrapper distribution build | **PASS** (SQLite and Postgres). `POST /api/deployment/git-connections {host: git.example.com, type: ambient, source: forgecli}` is 200 and the connection is in `gitConnections`; it runs `forgecli auth token` and `forgecli api --include …`; `GET /api/deployment/reachable-repos` lists `git.example.com/walk-org/service-a` and `service-b` | same |
| 13 | `forgecli` in the core build | **PASS**. 400 `unknown ambient source "forgecli": gh, git, auto` | same |
| 14 | Per-model pause | **PASS (CI)**. `pkg/orchestrator` and `pkg/cli` in the uncached `go` job | same |
| 15 | Task lifecycle: create (CLI) → approve | **PASS (live)**, all four daemons. `grain create -repo acme/widgets …` filed task 2; `grain approve 2` queued it; the task's JSON has no `team` | same |
| 16 | Lifecycle end to end (sandbox clone, push, PR, grain CI, merge queue, merge) | **PASS (e2e harness)**. Local `tests/e2e`, plus `TestMergeQueue*` and `pkg/daemon` in CI (merge batching now reads `grain_config.merge_batch_size`, b4) | same |
| 17 | Create through MCP, including proposals | **PASS (e2e harness / CI)** | same |
| 18 | Create through the UI | **PASS (CI)**. `ui e2e` job | same |
| 19 | Schedules and templates firing | **PASS**. The e2e schedule walkthroughs pass; a fresh daemon seeds the system schedules | same |
| 20 | Director / interactive chat | **PASS**. A fresh daemon files the director console session (task 1) | same |
| 21 | State-repo export / import round trip | **PASS (live)**. `grain export -data-dir … -out` of core/SQLite ("exported 10 files … team state repos: 0"), `grain import` into a fresh dir ("schema version: 24"), boot: tasks 1 and 2, all three git connections, the Gemini connection with its 11 models, the GCP key, the board, the memory document, the repo config, `defaultModel`, `targetRepos`, `mergeBatchSize`. The imported dir gets a new access code | same |
| 21a | `grain upgrade-postgres` on the baseline | **PASS**. The imported SQLite dir: **95 tables** moved in 2.0s (96 at bbc84069c, 97 last time: `team_repo_config` and `team_gcp_key` went and `gcp_key` came with b3; `team` went with b4). `db-config.json` written, the daemon booted on Postgres with the same tasks and the GCP key. Of `team*`/`*gcp*`, only `gcp_key` is left | changed (b3, b4) |
| 22 | `tests/security` route authorization | **PASS** (local and CI) | same |
| 23 | `-tenancy multi` | **PASS, unchanged**: not in `grain daemon -h`; a fresh dir with `-tenancy multi` starts single-tenant and logs "WARNING: starting anyway, ignoring 1 flag(s) this build does not define: -tenancy" | same |
| 24 | Deployment-level GCP keys, boards, memory | **PASS** on all four daemons. `grain gcp-key set ci-key --project p --account … FILE` gives `gcp-key:ci-key` (now a `gcp_key` row); `POST /api/boards` is 201; `PUT /api/memory/documents` is 200. All survive export/import and the move to Postgres | same |
| 25 | The deployment CLI and routes on `/api/deployment` | **PASS**, and b2's single default model checks out. `grain git-connections` lists `github.com bot` and `git.example.test anonymous`. `grain default-model gemini-3.1-pro-high` ("This deployment now runs on gemini-3.1-pro-high by default.") reads back from **both** `/api/deployment.defaultModel` and `/api/settings.defaultModel`: one value now, where they were a team field and a config field before b2. `PUT /api/deployment {targetRepos: […]}` reads back. `PUT /api/deployment/repos/acme/widgets/config` is 200 and reads back from `repo_config` | same (b2 checked) |
| 26 | **Schema upgrade to 24: a store at 19 (e4977c21d), 20 (66a303126), 21 (123c390a2), 22 (e1efc3e01) and 23 (bbc84069c)** | **PASS on all ten stores** (SQLite and Postgres, the refusal on both builds, the upgrade on alternating builds), at 79fb0647b; the eight 19–22 stores also passed to 23 at bbc84069c. The start exits 1 with "applying schema: database schema is older than this build: found 19 [20, 21, 22, 23], this build knows 24 -- nothing has been changed. To carry this deployment's rows across, stop grain and run `grain state -data-dir DIR prepare-upgrade`…". `diff -r` clean; `pg_dump` unchanged. `prepare-upgrade`: "rehearsed: 31 rows across 17 tables migrate from schema N into 24" (32/18 into 23). The next start logs "imported this deployment's rows … into a fresh store at schema 24" and "the config repository's dump was rewritten at schema 24", keeps `store.schemaN-<time>` (SQLite) or a `public_schemaN_<time>` schema (Postgres), and carries the task, both git connections, the Gemini connection (11 models), the memory document, `defaultModel`, `targetRepos`, **the GCP key** and **the repo config** (`setupCommand`, `promptExtension`, `defaultCapabilities`, now from `repo_config`). A second start is clean. **But** `prepare-upgrade` warns "tables/team_gcp_key.json [team_repo_config.json, and at 24 team.json] names no table this build has; every row in it would be ignored" about rows the fixups *do* carry (**bug J**). **Bundles:** `grain import` of each SQLite bundle (19–23) says "schema version: N, migrates to 24 when grain next starts on DIR", and the next start carries task 2 and the GCP key. **Each Postgres bundle (19–23), with its database restored from `pg_dump` first: `grain import` exits 1 with "the files are restored into DIR, but the postgres database it runs on is at schema N and this build knows 24 -- stop any daemon still running on that database, run `grain state -data-dir DIR prepare-upgrade`, then start grain", and writes no `store/` (bug I fixed)**. The next start refuses; `prepare-upgrade` then carries task 2 and the GCP key | fixed (I); new bug J |

## Previous failures and observations

| Item | Status |
|---|---|
| Bug I: `grain import` of a Postgres bundle one schema back reports the wrong version and leaves a stray SQLite store | **Fixed** (`7fe482be4`, 4870). Row 26: refused with a message naming `prepare-upgrade`, no `store/`, at 19, 20, 21, 22 and 23 |
| Row 26 at each bump | **PASS** at 22→23 and at 19/20/21/22/23→24, SQLite and Postgres; bug J (wording only) is new |
| `/api/config`'s `teams` (expected to go with 6-ii-b4) | **Still present**, by design in b4: a one-element list built from `grain_config`, the "team shape the frontend reads" (`Declared.jsx` and `NoGitConnectionNotice.jsx` still read it). Its `description` and `createdAt` are gone. The plan's b4 item says the remaining team plumbing "goes with the UI's"; no stage names it yet. `adminAccounts`/`isAdmin` remain as the deployment's admins (not residue) |
| Default-model mismatch (`gemini-3.1-pro` in the seed and the `connections import -h` example) | **Still failing** (row 10). b2 merged the two default models into one, but the seed's value is still one Gemini discovery doesn't serve |
| `grain git-ambient` with no arguments writes a second host-default github.com connection | **Still present**: "This deployment now clones, pushes and calls GitHub as this host's own gh login on github.com", and `grain git-connections` lists `github.com bot` and `github.com grain-team-ZGVmYXVsdA-c-…` |
| `GET …/git-connections` reports `status.host: "github.com"` for the anonymous `git.example.test` connection | **Still present** |
| `/api/agent-models` reports agy's "Please sign in to view available models" | **Still present** |
| Help and wording residue | **Mostly fixed by the UI rework; CLI and log residue still present.** **Fixed:** the Git connections tab's "for this team" / "this team's repositories" / "Add them on the Team tab" (`TeamGitPanel.jsx` is gone; `/settings/git` has none of it) and Settings' "Browser default or team default" (now "Browser default"). **Still present:** `grain export -h` "team state repositories"; `grain import -h` "running store migrations"; `grain gcp-key -h` → `unknown gcp-key subcommand "-h"`; the task detail's "Team" row and "Model: team default (…)" (`Declared.jsx`); New task's "leave it on the team's default", "connections of this team that serve …" and "You are a proposer on this team" (`NewTaskOverlay.jsx`, `EditTaskForm.jsx`, `UseTemplateOverlay.jsx`); the "not ready" log line's "a team admin of \"default\" adds one in Team settings -> Models & connections" (`pkg/daemon/agents.go:736`); connection names `grain-team-ZGVmYXVsdA-c-…` (`pkg/model/team_git_credential_names.go`) |
| `PUT /api/memory/documents` with no `text` answers 200 with `length: 0` and an empty `summary` | **Still present**, and it drops the `summary` it was given (`"s"` → `""`). The document is never listed afterwards, so nothing is stored. The 200 is the only problem |
| `/api/secrets/copy` answers 405 rather than 404 | **Still present** |

## Full suites

| Suite | Result |
|---|---|
| grain CI, full workflow, **uncached**, on bbc84069c (`retrigger_ci` `*`, run **0e19e212124a411c**, at d7c6e6d8e: bbc84069c plus this doc's placeholder) | **All 10 jobs green, no retry.** `go` passed its first attempt (9m17s): the first-attempt flake of checks #6 and #7 didn't recur. `ui e2e` 16m39s, `frontend` 4m36s, `installer` 4m21s, `postgres` 2m42s, `run host` 2m36s, `package cache` 2m5s, `relay image` 1m9s, `terraform` 48s, `lint` 28s |
| grain CI, full workflow, **uncached**, on 79fb0647b (`retrigger_ci` `*`, run **d2575bc69117bc43**, at 9f800396a: 79fb0647b merged into this branch) | **All 10 jobs green, no retry** (16m23s). `go` 9m15s first attempt, `ui e2e` 16m12s, `frontend` 4m42s, `installer` 4m16s, `run host` 2m34s, `postgres` 2m14s, `package cache` 2m5s, `relay image` 1m20s, `terraform` 52s, `lint` 32s. (`wait_for_checks` on the same commit started a second run, 5b63871bf3e4dbc7, alongside it) |
| grain CI, full workflow, on this report's commit (`wait_for_checks`) | This report's own commit: its grain CI verdict is the pull request's gate, recorded in the task's closing note |
| Local `go test -count=1 ./tests/e2e/` (no `-race`) | **ok on both heads**: bbc84069c 221s, 79fb0647b 230s. `go test -list` finds 137 top-level tests at bbc84069c and 133 at 79fb0647b (b4 deleted the multi-team tests). A pass/skip breakdown wasn't recorded: the runs weren't `-v` |
| Local `go test -count=1 ./tests/security/... ./google/...` | **bbc84069c: pass** (`tests/security` 47s; every `google/...` package `ok`, 8 packages). **79fb0647b: pass** (`tests/security` 41s; all 8 `google/...` packages `ok`) |

## Bugs

Fixed since the last check: **I** (`7fe482be4`, task 4870).

Still open: the default-model mismatch (bug 1 of the 10-18 candidate
qualification). Not re-filed; this run had no task-comment tool, and there
is no new evidence beyond row 10.

New this check (proposed as a task by this run, not auto-merge, against
`main`):

- **J. `prepare-upgrade` warns that rows the dump fixups carry "would be
  ignored".** On a store at 19–22, `grain state prepare-upgrade` prints
  "tables/team_gcp_key.json names no table this build has; every row in it
  would be ignored", and the same for `team_repo_config.json`. At 24 (after
  b4) it also prints it for `team.json`. Those rows aren't ignored: the 22
  fixup (`foldTeamRepoSettings`) and the 23 fixup (`foldDefaultTeamRow`)
  carry them, and after the start the GCP key, the repo config,
  `targetRepos` and the default model are all there. `MigrateDump` copies
  the dump, runs the fixups on the copy and leaves the source files in it.
  The rehearsal check (`pkg/staterepo/check.go:273`) then reports each
  file of a table the build lacks. The operator is told their GCP keys and
  repo settings will be lost when they won't. Repro in row 26. *Owner:
  follow-up to 6-ii-b3 (4862) and 6-ii-b4 (4863); the proposal was written
  before b4 landed, so it names `team.json` only as b4's expected case.*

## Observations, not filed

- `/api/config` still carries `tenancy: "single"`, a constant on a build
  with no other mode.
- A fresh dir still writes `hub-audit.log` (the local policy audit log,
  which 4826 says stays).
- At 24, `prepare-upgrade` of a dump at 23 also warns that
  `grain_config.json` row 0 "does not carry `agent_framework`" (and
  `agent_git_name`, `agent_git_email`), so the column "lands on its
  default". That's true and harmless here: the baseline's team row had
  none set, so the 23 fixup had nothing to fold.
- The baselines didn't set `agent_framework`, an agent git identity, a
  repair template or a non-default merge batch size on the team row, so
  b4's fold of those four was covered only by CI
  (`TestADumpAt23FoldsTheDefaultTeamRowOntoGrainConfig`), not live.
  `targetRepos` and the default model were covered live.
- 4903 moved `setup.sh`'s install paths (`/var/lib/grain/{data,scratch}`);
  `scripts/setup.sh` isn't in this check's flow table and wasn't run.

## Brief vs. tree

- The brief says to find in-flight stage tasks with `list_grain_tasks` and
  to comment on open bugs' tasks. This run had neither a task-listing nor
  a task-comment tool, the same as checks #2–#7. In-flight work was read
  from `git log origin/main`, `docs/single-tenant-reset.md` and the
  `grain/eu/task-*` branches on the remote.
- The brief's in-flight list has been overtaken: 6-ii-b2 (4861), 6-ii-b3
  (4862), bug I (4870) and, mid-run, 6-ii-b4 (4863) all landed. Schema is
  24, not 22. The brief expected `/api/config`'s `teams` to go with b4;
  b4 kept it as the frontend's team shape, built from `grain_config`.
- Practical notes held. One addition: when re-creating a baseline, remove
  the old `X.orig`/`X.pre` copy first, because `cp -a X X.orig` onto an
  existing directory nests the copy inside it. That cost this run one
  false alarm, a "lost" GCP key that turned out to be a stale copy.
