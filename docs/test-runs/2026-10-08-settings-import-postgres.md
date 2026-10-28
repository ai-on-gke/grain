# Walkthrough: Settings Import and State Repo on PostgreSQL

Plan: `docs/test-plans/2026-10-07-evening.md` §5. Task 2541 (filed by task 2516).

## Environment

- **Sandbox:** Debian 12 (bookworm), 4 vCPU, 7.8 GiB RAM.
- **PostgreSQL:** PostgreSQL 15.19 (`psql (PostgreSQL) 15.19 (Debian 15.19-0+deb12u1)`), running locally via UNIX socket/TCP, managed by `pkg/model/postgres/postgrestest`.
- **Go:** go1.26.6 linux/amd64.
- **Git:** git version 2.39.5.

## Tested Commit

- Merge base commit: `b25cc6efdc70509175f4dbc2cb645acfa00897a4` (`origin/candidate`).
- In-scope commit containing the fix: `f55660bd2` (`main` `908bc0a66`).
  Verified via `git branch -r --contains f55660bd2`: present in `origin/candidate` and `origin/grain/task-2541`.
- Walkthrough test commit: `7f18df7f3` on `grain/task-2541`.

---

## 1. Environment Verification

Confirmed PostgreSQL is installed and functional:
```bash
psql --version
# psql (PostgreSQL) 15.19 (Debian 15.19-0+deb12u1)
```

Ran existing Withheld test to verify setup:
```bash
GRAIN_REQUIRE_POSTGRES=1 go test -v -count=1 ./pkg/model/postgres/ -run TestApplyingSettingsKeepsTheGoogleOAuthClientSecret
# PASS: TestApplyingSettingsKeepsTheGoogleOAuthClientSecret (2.28s)
# ok   github.com/bwsalmon/grain/pkg/model/postgres 2.420s
```

---

## 2. Withheld Columns and Resets on PostgreSQL

Added comprehensive automated tests in `pkg/model/postgres/settings_import_walkthrough_test.go`:

### 2.1 Withheld Columns
`pkg/staterepo/withheld.go` defines:
```go
var withheldColumns = map[string][]string{
    "grain_config": {"google_oauth_client_secret"},
}
```
Tested in `pkg/model/postgres/withheld_settings_test.go`:
- Seeded `grain_config` with `google_oauth_client_secret`.
- Exported configuration.
- Imported modified settings back into PostgreSQL with withheld column omitted or modified.
- Verified running `google_oauth_client_secret` value survives untouched.

### 2.2 Resets with NULL Keys on PostgreSQL
Added `TestResetWithNullKeyOnPostgres` in `pkg/model/postgres/settings_import_walkthrough_test.go`:
- Tested `TableResets` / `TableResetsWithColumns` on PostgreSQL where a composite key contains `NULL` values.
- In SQL, `col = NULL` evaluates to `UNKNOWN` (false), which would fail to find the existing row unless `IS NOT DISTINCT FROM` is used.
- Verified that `pkg/staterepo/resets.go`'s `IS NOT DISTINCT FROM ?` properly matches rows with `NULL` keys and executes resets correctly on PostgreSQL.
- Outcome: **PASS** (`TestResetWithNullKeyOnPostgres`, 1.92s).

---

## 3. SQL Sweep of `pkg/staterepo`

Swept `pkg/staterepo` for SQLite-specific SQL syntax:
```bash
git grep -nE "IS \?|OR REPLACE|GLOB|IFNULL|rowid|\|\|" pkg/staterepo
```

### Classification Findings

| Syntax | File & Line | Classification | Rule / Notes |
|---|---|---|---|
| `IS ?` | `pkg/staterepo/resets.go:121` | **Fixed** | Replaced by `IS NOT DISTINCT FROM ?` in commit `f55660bd2`. Portable on Postgres 15+ and SQLite. |
| `IS ?` | `pkg/staterepo/withheld.go:111` | **Fixed** | Replaced by `IS NOT DISTINCT FROM ?` in commit `f55660bd2`. Portable on Postgres 15+ and SQLite. |
| `OR REPLACE` | None in `pkg/staterepo` | **Portable** | Not used in queries in `pkg/staterepo`. |
| `GLOB` | None in `pkg/staterepo` | **Portable** | Not used in `pkg/staterepo`. |
| `IFNULL` | None in `pkg/staterepo` | **Portable** | Not used in `pkg/staterepo` (code uses `COALESCE` where applicable). |
| `rowid` | `pkg/staterepo/check.go:48`, `decl.go:340` | **Portable** | Only appears in Go code comments discussing SQLite table dump sorting. No SQL statement executes `rowid`. |
| `\|\|` | None in SQL queries in `pkg/staterepo` | **Portable** | Only appears in Go boolean logic (`||`). |
| `PRAGMA table_info(...)` | `pkg/staterepo/dump.go:823` | **Rewritten by Postgres adapter** | Handled by `pkg/model/postgres/adapter.go:482` (`pragmaTableInfoRe`), which rewrites `PRAGMA table_info(...)` into queries against `information_schema.columns` and `information_schema.table_constraints`. |
| `sqlite_master` | `pkg/staterepo/dump.go:802` | **Rewritten by Postgres adapter** | Emulated in PostgreSQL via an adapter view created in `pkg/model/postgres/schema.go`. |

---

## 4. Round Trips

Implemented and verified both round-trip scenarios:

### 4.1 Postgres -> Postgres Round Trip
Test: `TestSettingsRoundTripPostgresToPostgres` in `pkg/model/postgres/settings_import_walkthrough_test.go`:
- Populated a Postgres store with comprehensive configuration:
  - OAuth configuration (`grain_config`)
  - Teams with git connections (`team`, `team_git_connections` with multi-host/GHE configurations)
  - Schedules (`schedule`)
  - Templates (`template`)
  - Suites (`suite`)
  - Users (`user_account`)
  - Repo configs & Team repo configs (`repo_config`, `team_repo_config`)
- Exported Postgres store 1 to directory `dir1`.
- Imported `dir1` into fresh empty Postgres store 2.
- Exported Postgres store 2 to directory `dir2`.
- Compared directory trees `dir1` and `dir2`.
- Outcome: **PASS** — Byte-for-byte identical across all exported files.

### 4.2 SQLite -> Postgres Round Trip (Operator Migration Path)
Test: `TestSettingsRoundTripSQLiteToPostgres` in `pkg/model/postgres/settings_import_walkthrough_test.go`:
- Populated a SQLite store with the comprehensive settings dataset above.
- Exported SQLite store to directory `dirSQLite`.
- Imported `dirSQLite` into fresh empty Postgres store 1.
- Verified that all entities (OAuth, Team with Git connections, Schedules, Templates, Users) were faithfully restored in Postgres.
- Exported Postgres store 1 to `dirPG1`.
- Imported `dirPG1` into fresh empty Postgres store 2, and exported to `dirPG2`.
- Verified that `dirPG1` and `dirPG2` are byte-for-byte identical.
- Verified that declaration files (`settings/schedules.yml`, `settings/templates.yml`, `settings/suites.yml`) are byte-for-byte identical between SQLite export and Postgres export.
- Outcome: **PASS**.

---

## 5. Failure Visibility (Daemon Serving Preserved)

Tested in `TestFailedSettingsApplyLeavesDaemonServing` in `pkg/model/postgres/settings_import_walkthrough_test.go`:
- Configured a real `staterepo.StateManager` on a PostgreSQL database.
- Injected a corrupted settings commit (invalid JSON in `tables/grain_config.json`).
- Cycled the state manager to apply the corrupted commit.
- **Observed Behavior:**
  - `Apply` logged the refusal and error:
    `staterepo: refusing to apply the settings that just arrived, keeping this deployment's database as it is: staterepo: parsing .../tables/grain_config.json: invalid character 'N' looking for beginning of value`
  - The StateManager returned `staterepo.ErrNotApplied` rather than crashing, panicking, or looping.
  - The error is recorded and surfaced in `repo.Status().Error`.
  - The daemon remained completely healthy and serving, with the underlying database left intact.
- Outcome: **PASS**.

---

## 6. Fixes and Improvements

### Finding: Schedule YAML Field Drift (`auto-approve-proposals`)
During round-trip testing between SQLite and Postgres, schedule YAML declarations (`settings/schedules.yml`) exposed a field formatting difference:
- In `pkg/staterepo/decl.go`, `booleanKeys` defines keys that must be formatted as booleans (`true`/`false`) rather than integers (`0`/`1`).
- `auto-approve-proposals` was missing from `booleanKeys`. On SQLite (which stores booleans as integer 0/1), it was serialized as `auto-approve-proposals: 0` or `1`, whereas on Postgres (storing native boolean), it was serialized as `true`/`false`.
- **Fix:** Added `"auto-approve-proposals": true` to `booleanKeys` in `pkg/staterepo/decl.go`. This guarantees identical canonical YAML serialization across both database engines.

---

## 7. CI Coverage Verification

The walkthrough tests are housed in `pkg/model/postgres/settings_import_walkthrough_test.go`.
Because `.grain/workflows/tests.yml` executes:
```yaml
run: go test -count=1 -timeout 20m ./pkg/model/postgres/... ./pkg/model/
```
the tests run directly in the `postgres` CI job with `GRAIN_REQUIRE_POSTGRES=1`.

Full test suite execution:
```bash
GRAIN_REQUIRE_POSTGRES=1 go test -count=1 ./pkg/model/postgres/...
# ok   github.com/bwsalmon/grain/pkg/model/postgres 50.037s
```
Duration was ~50 seconds, comfortably well below the 20-minute job timeout.
All `pkg/staterepo/...` tests also pass cleanly.

---

## 8. Grain CI Verdict

Grain CI run `44c888616bc2330c` passed all required check jobs cleanly on commit `7f18df7f3`:

| Job | Result | Duration |
|---|---|---|
| `frontend` | Success | 5m50s |
| `go` | Success | 18m9s |
| `installer` | Success | 3m54s |
| `lint` | Success | 25s |
| `package cache` | Success | 2m4s |
| `postgres` | Success | 2m5s |
| `terraform` | Success | 46s |
| `ui e2e` | Success | 15m8s |

Verdict: **All green / Success**.

