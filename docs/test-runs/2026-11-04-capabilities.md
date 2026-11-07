# Capabilities: retirements, gcp sets over existing accounts, server-drop reporting, the custom connection removal

- **Plan:** `docs/test-plans/2026-11-04.md` §8 (PRs #493, #613, #625, #614, #471, #631, #647, #594, #645, #643). Starts from [`2026-11-03-capabilities-ci-repo.md`](2026-11-03-capabilities-ci-repo.md).
- **Date:** 2026-11-07, from 03:10 UTC.
- **Task:** 3074, branch `grain/049f1e09/task-3074-walkthrough-capability` on `ai-on-gke/grain`, base `main` at `c6004bc9` (schema 119 when the walk started; 120 with the fix below).
- **Environment:**
  - Debian 12 sandbox, `linux/amd64`, 7 vCPU / 30 GiB, Docker on the host. Go `go1.26.10`, Node 20.19.
  - `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain` (rebuilt after the fix).
  - `grain93`: `go build ./cmd/grain` in a worktree at `0e33f85f` (`SchemaVersion = 93`).
  - `sqlite3` and `postgresql-client` from apt. PostgreSQL 16 from `docker run postgres:16` on `127.0.0.1:55432`.
  - Daemons ran with `-disable-native-sandboxes -sandbox-port-base=0`. Item 1 starts no runs.
  - **No GCP.** GCP IAM/STS is not reachable from the sandbox. Nothing below calls Google. Where gcp mints come up, they use fakes, and the item says which.

## Outcome

| §8 "not proved" item | Result |
|---|---|
| 1. Upgrade from a schema-93 store holding every retired grant shape | **Pass after one fix (F1).** Every grant on tasks, templates, schedules, deployment and repository defaults, repository CI capabilities and console sessions is rewritten to its server grant. The gcp sets are seeded from the pool and the `gcp_key` row, and the custom connection is disabled and kept. SQLite and PostgreSQL give identical results, and so do the in-place migration and the state-repo dump route. One surface was missed: a bare `grain-ci` in the capability policy's catalogue defaults stayed bare (migration 96 never touched the policy). Fixed with migration **120** and regression tests. F2: a schema-93 deployment can save a catalogue that its own export then refuses. That refusal survives the upgrade (filed). |
| 2–5 | See the sections below. |

## Findings

| # | Finding | Status |
|---|---|---|
| F1 | **Migration 96 left `grain-ci` bare in `grain_config.capability_policy`.** 96 rewrote `grain-ci` in the grant tables, default lists, console sessions and `repo_config.ci_capabilities`, but not in the capability policy. Every other retirement in the range rewrites the policy's rules and catalogue defaults too: 97 (`rewriteCapabilityPolicyGCPKey`), 100, 101 and 103 (`rewritePolicyRenames`), and later 115 and 118. So a catalogue whose `defaults.ci` (or task, or per-repository) list named `grain-ci` kept the bare alias through every upgrade. That happened in place on SQLite and on PostgreSQL alike, and on the dump route (`staterepo.ImportMigrated` replays the same `Up`s through `model.ApplyDataMigrations`). The alias still resolves to `grain#ci` today. It is only "accepted for one release", though, and Settings → Capabilities keeps flagging it as deprecated. Repro: [`seed93.sh`](2026-11-04-capabilities/seed93.sh) on a `grain93` daemon, then start the current binary on that data dir. [`grants-119-inplace.txt`](2026-11-04-capabilities/grants-119-inplace.txt) line 70 shows `"grain-ci"` under `catalogue.defaults.ci`. | **Fixed.** Migration **120** (`pkg/model/grain_ci_policy_migration.go`) runs `rewritePolicyRenames({grain-ci: grain#ci}, grain#ci)`, the same helper migration 100 uses. It changes no table. Regressions: `TestMigration120RewritesGrainCIInThePolicy` (SQLite and PostgreSQL) and `TestStateRepoImportRewritesGrainCIInThePolicy` (SQLite, dump route). Both SQLite tests fail without the migration (`defaults.ci = [… grain-ci]`). The PostgreSQL test was run against the Docker PostgreSQL (`GRAIN_POSTGRES_TEST_URL`). After the fix, [`grants-120-inplace.txt`](2026-11-04-capabilities/grants-120-inplace.txt) and [`grants-120-dump-import.txt`](2026-11-04-capabilities/grants-120-dump-import.txt) both carry `grain#ci={}`. |
| F2 | **The API saves a catalogue that its own export refuses.** On 93, `PUT /api/capability-policy` accepted `inProcess: {image-signing: {enabled: false}}`. `PUT /api/repos/acme/widgets/ci-capabilities` had already stored `image-signing`, and neither request checks the other. The export writes both into `settings/capabilities.yml`, and `grain93 state check` on that export fails: `defaults.ci: capability image-signing is disabled in settings/capabilities.yml`. After the upgrade (either binary), the in-place store's own schema-119/120 dump fails `grain state check` the same way (`… release#image-signing={} is disabled … (in_process.image-signing, the capability it replaced)`). `grain state prepare-upgrade` refuses to arm (`this build could not take the rows exported from the store at schema 93`). A deployment in this state cannot change `settings/capabilities.yml` in a config-repo PR until somebody fixes the contradiction by hand. | **Filed** (see "Follow-ups"). The fix belongs in `Client.SetCapabilityPolicy` and the repo-capability writes (validate the merged catalogue the export will write), not in a migration. |
| O1 | A data dir holding `schema-upgrade/` but no store (deleted by hand) starts a fresh, empty store. The dump is not imported ("the store is already at schema 120 and … schema-upgrade is still here"), and the empty database is then exported over the state repository. The prior dump is the preceding commit, so nothing is lost for good. It is not a supported flow. | Noted. |
| O2 | The 93 export writes `settings/capabilities.yml`'s `defaults` from `grain_config.default_capabilities` and `repo_config`, not from the policy's own catalogue defaults. So the catalogue defaults a dump-route import arrives with are those lists, not what `capability_policy.catalogue.defaults` held (the only difference in [`grants-120-inplace-vs-dump-import.diff`](2026-11-04-capabilities/grants-120-inplace-vs-dump-import.diff) besides the task rows, which a dump never carries). That is 93's own export, unchanged by this range. | Noted. |

## Item 1: upgrade from schema 93

### Building the store

[`seed93.sh`](2026-11-04-capabilities/seed93.sh) fills a fresh `grain93 daemon` through its own API wherever the schema-93 API accepts the grant, and with sqlite3 (or psql) where it refuses:

| Surface | Retired shapes | How |
|---|---|---|
| `gcp_key` + pool | `deploy` set (`walk-deploy`, 1 account, 2 h); pool `grain-sa@walk-proj` ×2 | `POST /api/gcp-keys`, `PUT /api/config` |
| task 2 | `gcp-key:deploy`, `gcp-key`, `claude-/openai-/gemini-proxy`, `gemini-key` (`label`); `grain-ci` (`default`, from CI mode `grain`) | `POST /api/tasks` |
| task 2 | `image-signing`, `registry-push`, `package-publish` | sqlite3. 93 refuses CI-only grants on a task |
| `template-1`, `sched-1` | the task's six, plus `grain-ci` and the three release names | API, then sqlite3. 93 refuses `grain-ci` by hand (`grain-ci cannot be attached by hand`) |
| `repo_config` acme/widgets | default `gcp-key:deploy, claude-proxy, gemini-key`; CI `gcp-key, claude-proxy, image-signing, registry-push, package-publish, gemini-key` | `PUT …/capabilities`, `…/ci-capabilities`. 93 refuses `gcp-key:deploy` for CI |
| deployment defaults | `gcp-key, openai-proxy` | `PUT /api/config` |
| console session `retired-session` (task 3) | the task's six, plus `grain-ci`, `image-signing` (sqlite3) | `POST /api/console-sessions` |
| capability policy | catalogue defaults (task/CI/per-repo) naming all five families; `inProcess` entries for `gcp-key`, `claude-proxy`, `image-signing` (disabled), `gemini-key`, `grain-ci` | `PUT /api/capability-policy`. Rules take capability references only, so 93 refuses a rule on a bare name |
| custom connection | `{"id":"my-custom","framework":"custom","command":"/usr/local/bin/my-agent --prompt-file {prompt}"}` beside a `claude` one | `PUT /api/config` |

[`dump-grants.sh`](2026-11-04-capabilities/dump-grants.sh) writes one line per grant from either store kind, and redacts secrets. Before: [`grants-93.txt`](2026-11-04-capabilities/grants-93.txt). The PostgreSQL store seeded by the same script, [`grants-93-postgres.txt`](2026-11-04-capabilities/grants-93-postgres.txt), is identical apart from the session task.

### Upgrading

| Route | Command | Result |
|---|---|---|
| In place, SQLite | `grain daemon -data-dir <copy>` | `migrated the store in place from schema 93 to 119` (120 after the fix). [`grants-93-vs-119-inplace.diff`](2026-11-04-capabilities/grants-93-vs-119-inplace.diff) before the fix, [`grants-93-vs-120-inplace.diff`](2026-11-04-capabilities/grants-93-vs-120-inplace.diff) after |
| In place, PostgreSQL | `grain daemon -postgres-url …` on the 93 database | Same log line. [`grants-119-postgres.txt`](2026-11-04-capabilities/grants-119-postgres.txt) is identical to the SQLite result apart from the session task's row order |
| `grain state prepare-upgrade` | after `grain93 state sync` | Refused on the seeded store (F2). With the contradiction removed (`image-signing` enabled), it rehearsed and armed (`114 rows across 23 tables migrate from schema 93 into 119`), and the start that followed matched the in-place result line for line |
| Dump route | [`import-dump.go`](2026-11-04-capabilities/import-dump.go): `staterepo.MigrateDump` + `staterepo.ImportMigrated` into a fresh store, then one daemon start | [`grants-120-dump-import.txt`](2026-11-04-capabilities/grants-120-dump-import.txt). Templates, schedules, defaults, CI capabilities, sessions, sets, `mcp_servers` and the custom connection match in place. Task rows are not in a dump. The catalogue defaults differ (O2) |
| Fresh 120 daemon over the 93 state repo | `state-repo/` copied into an empty data dir | Refused by design: `repository is at schema 93, this build knows 120 … run the build that wrote it` |

What the diff shows, grant by grant (each holds on every surface listed above):

| Schema 93 | Schema 120 |
|---|---|
| `gcp-key` | `gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}` |
| `gcp-key:deploy` | `gcp#gcp-key={"permission_set":"deploy","primary":true}`. The seeded `deploy` set has `key_path: /home/debian/.gcp-service-account-deploy.json`, so both primaries coexist. Only the default path activates gcloud, as on 93 |
| `claude-proxy` / `openai-proxy` / `gemini-proxy` | `model-proxy#model-proxy={"vendor":"claude"\|"openai"\|"gemini"}` |
| `image-signing` / `registry-push` / `package-publish` | `release#image-signing={}` / `#registry-push={}` / `#package-publish={}` |
| `gemini-key` | `gcp#gemini-key={}`, with `gemini_key: {admin: minter, project: walk-proj}` seeded on the gcp entry |
| `grain-ci` | `grain#ci={}`, and in the policy's catalogue only after migration 120 (F1) |
| gcp entry | `permission_sets` `sandbox-agent` (`grain-sa-0/1@walk-proj`, 24 h, admin `minter`) and `deploy` (2 h, admin `minter-deploy`), `adopt_legacy: true`, `env` naming `$secret:gcp-key-minter[-deploy]`, plus the deprecation `notice`. The attach is then refused, because neither secret exists (`capability server references secrets that are missing or empty: gcp-key-minter, gcp-key-minter-deploy`), exactly as the 93 `gcp-key` would have failed to mint |
| `model-proxy` external entry in `mcp_servers` | removed (in-process now) |
| `my-custom` connection | kept, with `"disabled": true` and `"disabledReason": "custom command connections were removed in v3.2.0; replace it with an Antigravity, Claude or Codex connection"` |
| `inProcess.image-signing: {enabled: false}` | kept. `judgeRetiredInProcess` binds `release#image-signing` to it (`… disabled … (in_process.image-signing, the capability it replaced)`), so the operator's switch still holds |

The session's task (3) is re-synced from the session on the first start. It picks up `grain#ci` and `release#image-signing` from the two names sqlite3 added to the session, plus `api:ci-read` (default). `release#image-signing` is CI-only, and minting refuses it for a task. It is only there because the seed wrote what 93's API refuses.
