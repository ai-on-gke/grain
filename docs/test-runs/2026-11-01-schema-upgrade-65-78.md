# Schema upgrade 65–78 (and on to 82) on SQLite and Postgres — test run 2026-11-01

Plan: [`docs/test-plans/2026-11-01.md` §9](../test-plans/2026-11-01.md) ("Fourteen schema migrations in a day"). Filed by task 2084, run as task 2224.

**Result: fail on `main`, fixed on this branch.** Two bugs were found and fixed (B1, B2), each with a regression test. Two smaller gaps were filed as follow-ups (G1, G2). Everything else in the plan passed on both databases.

## Environment

| | |
|---|---|
| Old build | `8bdfa3a5`, built from a worktree at `/var/tmp/old`. `grain schema-version` = **65**, not 64: 65 (issue import) is already in it. |
| New build | `main` at `da97fdb0`, plus this branch's fixes. `SchemaVersion` = **82**, so the upgrade crosses 66–82: the plan's 65–78 plus 79 (`pending_config_change`) and 80–82 (`grain-log`, `deployment-health`, `db-read`/`db-write` grant rewrites). |
| Unfixed new build | `main` at `da97fdb0` (`/var/tmp/grain-main`), used to confirm each bug reproduces. |
| SQLite | `modernc.org/sqlite` v1.57.0 in the daemon; `sqlite3` CLI 3.40.1 for `.dump`. |
| Postgres | PostgreSQL 15.19 (Debian 15.19-0+deb12u1): a local `initdb` cluster on `127.0.0.1:55432`, with `pg_dump` from the same install. |
| Go | 1.26.10 |
| Daemon flags | `grain daemon -data-dir D -ui-addr 127.0.0.1:P -sandbox-port-base=0 -disable-native-sandboxes [-postgres-url …]`. Internal grain/config, with no GitHub credential and no sandbox backend. |

### Seeding

All seeding was done on the old (65) daemon.

- **Through the API / CLI:** 11 no-repo tasks, each holding one retired in-process grant: `team-broadcast`, `dispatch-control`, `pipeline-control`, `memory`, `release-notes`, `self-upgrade`, `test-quarantine`, `grain-log`, `deployment-health`, `db-read` and `db-write`. The tasks were then approved, deferred, closed, paused and commented on, which gave `proposed`, `queued` (several), `deferred`, `closed` and `paused` states. The system schedules the daemon seeds itself carried real `memory` and `release-notes` grants.
- **By SQL on the stopped store, then `grain state sync`:**
  - **Why not the API:** an internal-mode 8bdfa3a5 daemon answers every settings, template, schedule, repository and console-session write with `409 config_managed_externally` and a grain/config patch.
  - **What was seeded:**
    - `grain_config`: `default_capabilities` holding retired names, the unslugged `task_branch_template` `grain/{deployment}/task-{id}`, and two standing console sessions. One session holds both `memory` and `api:memory`, to exercise the "already held" branch.
    - Two `repo_config` rows. `acme/widgets` has the issue importer on, plus retired `default_capabilities` and `ci_capabilities`.
    - Two templates; one has four retired grants.
    - Two schedules, one of them template-backed with three retired grants.
    - `task_observation` rows giving `awaiting_reply`, `failed` and `merged`.
  - **Not reached:** `running` and `awaiting_submit`, which need a sandbox backend.
  - **Export:** `grain state sync` then committed the rows to the internal grain/config. That gave the `tables/*.json` + `settings/*.yml` export at schema 65 used below.

Each seeded store was snapshotted before the upgrade: a copy of the SQLite data dir, and for Postgres `createdb -T grain grain65` plus `pg_dump`. Every diff below is a per-table, per-row, per-column comparison on the columns both sides share (`rowdiff.py` / `rowdiff_pg.py`), checked against the full `sqlite3 .dump` / `pg_dump` diff.

## 1. Upgrade 65 → 82 with realistic rows

### What happens on `main` before this branch's fixes

| Path | SQLite | Postgres |
|---|---|---|
| `scripts/setup.sh` path: `grain state prepare-upgrade`, then start | **FAIL (B1).** `this build could not take the rows exported from the store at schema 65 … NOT NULL constraint failed: schedule.keep_running` | **FAIL (B1).** Same error: the rehearsal's scratch store is SQLite. |
| Plain binary swap, then start | The store is migrated in place to 82, then the daemon **refuses to start** (`the state repository holds a dump this build cannot read … repository is at schema 65`). See G2. | Same. |
| `grain export` (old) → `grain import` (new) | pass | not run |

### With the fixes

`prepare-upgrade` prints `prepared 65 82` and reports 131 rows (SQLite) / 93 rows (Postgres) in the rehearsal. Both daemons then start: `Store.Init` migrates in place, and `migrateStateRepo` rewrites the dump at 82 ("Migrate grain state to schema 82").

Every changed row, attributed (SQLite and Postgres identical apart from task ids and boolean spelling):

| Migration | What changed in the seeded store |
|---|---|
| 65 | Already applied at 8bdfa3a5: the `acme/widgets` issue-import settings (`1, acme/widgets-issues, grain, 1, 900`) carried across unchanged; `acme/gadgets` stays `0,'',…`. |
| 66 | `grain_config.session_sandbox_idle_ttl_ms` added = 0 |
| 67, 68, 69, 71, 72, 75, 76 | `task_grant`, `template_grant` and `schedule_grant` rows go from `team-broadcast` → `api:team-broadcast`, … `test-quarantine` → `api:test-quarantine`. The system schedules' `memory` → `api:memory` and `release-notes` → `api:release-notes`. `default_capabilities` is rewritten on `grain_config` and `repo_config` (JSON list in, CSV out, which is the native form; `splitCapabilities` reads both). In the console sessions, `memory` + `api:memory` collapse to one `api:memory`, and the session JSON is re-serialised with sorted keys, which means the same thing. |
| 70 | `grain_config.settings_locked` added = 0 |
| 73 | `task.priority` = 5 on all 14 tasks; `template.priority` = 0 |
| 74 | `keep_running` = 0 on every task, template and schedule |
| 77 | `grain_config.new_task_form` = `''` |
| 78 | `task_branch_template` `grain/{deployment}/task-{id}` → `grain/{deployment}/task-{id}-{slug}`. Existing tasks' stored `branch` values are unchanged. |
| 79 | `task_observation.pending_config_change` added, NULL |
| 80 | `grain-log` → `https://grain.dev/capabilities/grain#journal={}` (tasks, the director, repo defaults) |
| 81 | `deployment-health` → `api:deployment:read` + `…/grain#health={}` (tasks, template, defaults) |
| 82 | `db-read` / `db-write` → `…/grain#sql={"write":false}` / `{"write":true}` |
| (schema re-apply, no version) | New empty tables `task_run_relayed_call`, `task_run_sandbox_key`, `task_run_startup` and `task_run_wait_ledger`, created by `applySchema`. |

**Not caused by a migration** (checked one by one):

- `system:pipeline-steward` / `system:steward-audit` bodies, and the director/session task bodies: built-in prompts refreshed at startup (the state-repo commit after the migration touches only `settings/schedules.yml` prompt text).
- `task_run`, `task_run_startup` and `task_observation.streak_count` / `last_failed_at` on the queued tasks: dispatch attempts that fail because there is no sandbox backend. The old daemon did the same.
- `secret_resolution_audit` +1 per start.
- On Postgres, the first start created the two console-session tasks and closed the director (`console-session-reset`). The old SQLite daemon did exactly that on its restart after the same seed, so this is the session reconciler, not the upgrade.

**UI:** I rebuilt the binary with the UI embedded (`npm ci && npm run build`) and loaded each page in Chromium (Playwright, 1440 px) with the access code, on SQLite and Postgres. Pages: `/`, `/inbox`, `/sessions`, `/tasks/<id>`, `/schedules`, `/templates`, `/suites`, `/boards`, `/repos`, `/repos/acme/widgets`, `/ci`, `/memory`, `/system`, `/system/metrics`, `/system/pipeline`, `/settings`, `/settings/models`, `/settings/config-repo`, `/settings/ci` and `/settings/agents`.

- **Load:** every page returned 200, with no page errors, no console errors and no 5xx API responses.
- **Content checks:** `/schedules` lists both seeded schedules; `/templates` the seeded template; a task page shows `api:team-broadcast`; `/sessions` both standing sessions. The nav counts show proposed / waiting / asked / failed / deferred / closed / merged.

## 2. Restart idempotence — pass

A second start of the new build on the upgraded store ran no migration and wrote no state-repo commit. On SQLite and Postgres the only row changes were `secret_resolution_audit` +1 and the dispatch attempts described above. No schema, config or grant row changed.

## 3. State-repo import of the 8bdfa3a5 export — fail on `main` (B2), pass with the fix

The schema-65 `tables/*.json` / `settings/*.yml` the old daemon exported were imported into a fresh 82 store through `staterepo.CheckMigration` (`grain state check -migrate`, the same `importInto` that `ImportMigrated` runs for the note-based rebuild and the Postgres move). The bundle path (`grain export` at 65 → `grain import` at 82) migrates its store in place and passed.

| Threshold | Applied? |
|---|---|
| `dump.go` `found < 78` slug normalisation | yes: once, and only for the exact unslugged default |
| Capability rewrites (67–82), template and schedule grants | yes: `decl.go` canonicalises them on every import (`CanonicalLegacyGrants`); idempotent |
| Capability rewrites (67–82), `grain_config` / `repo_config` `default_capabilities` and console sessions | **no on `main` (B2)**: the retired names arrived in the 82 store as they were. Fixed: applied once, keyed by each migration's own number. |
| `settings_locked`, `session_sandbox_idle_ttl_ms`, `new_task_form`, `priority`, `keep_running`, issue import | not thresholds: absent keys take the column default (0 / '' / 5) |

## 4. Downgrade refusal — pass

The 8bdfa3a5 binary on the 82 store exits at once on SQLite and on Postgres with `applying schema: database schema is newer than this build: found 82, this build knows 65`. The store stays at 82, no row changes and the state repo is untouched.

## 5. Mismatched migration numbers — none found

These all agree:

- the `Version:` entries in `pkg/model/migrations.go` (65–82, contiguous);
- the per-version comments above `SchemaVersion` in `pkg/model/schema.go`;
- the version table in `pkg/model/sqlite/legacy_grant_migration_test.go` (67, 68, 69, 71, 72, 75, 76, 80, 81, 82);
- the stamps in the 74/77 → 78 tests in `pkg/model/sqlite/store_test.go`, and `TestADumpBefore78UpgradesUnsluggedTaskBranchTemplate`;
- the `dump.go` `< 78` threshold;
- the migration numbers cited in `docs/agent-api.md`, `docs/manual-mode.md` and `docs/design/unified-grants.md`.

The "schema 70" in the commit messages of `98eb06ac`, `17fcb581`, `c6855867` and `9f7044fe` is history only; no code or doc repeats it.

## Bugs

### B1 — `prepare-upgrade` refuses every pre-74 deployment that has a schedule (fixed)

`grain state prepare-upgrade` (`scripts/setup.sh` step 6) has the new build export the old store's rows, then rehearses importing them.

- **Cause:** `staterepo.declEntry` wrote every field the current schema declares. For a column the old store lacks, that meant `keep-running: null` and `priority: null`, which the NOT NULL columns then refuse.
- **Effect:** the rehearsal fails, and `setup.sh` either stops the upgrade or, with `GRAIN_ALLOW_STORE_RESET=1`, sets the store aside.
- **Deterministic repro:** see 8bdfa3a5 above, seed one schedule, then run `grain-main state -data-dir D prepare-upgrade`.
- **Fix:** `decl.go` leaves out a one-column field whose column the row was not read with, so the import gives it the column default.
- **Test:** `TestAStoreFromBeforeKeepRunningExportsSchedulesThisBuildCanImport` (`pkg/staterepo`). It drops `keep_running` / `priority` from a store, exports and re-imports it.

### B2 — an old dump imported at 82 keeps retired grant names in the default lists and console sessions (fixed)

`importInto` canonicalised only the template and schedule grants. `grain_config.default_capabilities`, `repo_config.default_capabilities` and `grain_config.console_sessions` came through with `memory`, `dispatch-control`, `db-read` and the rest.

- **Fix:** a new `model.ApplyDataMigrations(ctx, tx, from)` runs the `Up` rewrite of every migration past the dump's schema. All of them are idempotent grant rewrites. `importInto` calls it for a dump stamped below `SchemaVersion`. The threshold is the migration's own number, so `dump.go` cannot drift from `migrations.go`.
- **Test:** `TestADumpAt64ImportsWithEveryMigrationsRewrite` (`pkg/staterepo`). The fixture `pkg/staterepo/testdata/upgrade-schema-64` is the real 8bdfa3a5 export, trimmed to the seeded config, with the 65 issue-import keys removed and stamped 64. The test checks:
  - 65: the issue importer is off;
  - 70: `settings_locked` defaults to false;
  - 73 / 74: priority and `keep_running` default to 0;
  - 78: the unslugged template is upgraded;
  - 67–82: no retired name survives anywhere, and console sessions are deduplicated.

Both tests fail on `main` and pass on this branch.

### Gaps filed rather than fixed here

- **G1:** `repo_config.ci_capabilities` is not rewritten by any grant migration. It still holds `["release-notes"]` after the upgrade, through both the in-place and the import paths. Fixing it needs a new migration number.
- **G2:** a plain binary swap (no `setup.sh`) migrates the store in place, then refuses to start on the stale 65 dump. That leaves a store neither build will run until someone runs `grain state reformat`; I checked that `grain state reformat` + restart recovers. Either the daemon should check the dump before it migrates, or an in-place migration from N should authorise `migrateStateRepo` the way the upgrade note does. Separately, `grain state reformat`'s "if the store is at the old schema too … move it aside" hint is out of date: the store migrates in place.
