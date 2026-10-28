# Test Run Report: PostgreSQL Storage Engine & SQLite-to-PostgreSQL Upgrade Pipeline

**Date**: 2026-09-23  
**Status**: PASSED (All Automated & End-to-End Walkthrough Tests Verified)  
**Target Branch**: `grain/task-411`  
**Test Scope**: PostgreSQL backend, SQLite export/import upgrade pipeline, CLI and REST aliases, sequence generator synchronization, schema migration fidelity, and daemon operations.

---

## Executive Summary

A comprehensive test walkthrough and verification suite was executed against Grain's PostgreSQL storage engine and the SQLite-to-PostgreSQL upgrade pipeline. The test plan verified full data fidelity across complex multi-entity databases, sequence synchronization post-migration, CLI subcommand aliases, REST API endpoints, live daemon query execution, and rollback behaviors.

During the walkthrough, several core bugs in dialect translation, view identification, boolean type conversion, and sequence synchronization were uncovered and resolved. All unit, integration, CLI, UI, and stress test suites now pass cleanly.

---

## Environment & Architecture

- **Operating System**: Linux (Debian 12 Sandbox VM)
- **Database Backends**:
  - SQLite (WAL mode, foreign keys enabled)
  - PostgreSQL 15 (System Daemon & Isolated Embedded Instances)
- **Tooling**: Go 1.24+, `psql`, `grain` CLI & Daemon binary
- **Test Packages**:
  - `pkg/model/postgres/...` (adapter, embedded manager, config, upgrade, stress tests)
  - `pkg/staterepo/...` (export, import, schema migration, column mappings)
  - `pkg/ui/...` (REST endpoints, database configuration API, migration endpoints)
  - `cmd/grain/...` (CLI subcommands, daemon startup, flags, env vars)
  - `pkg/model/sqlite/...` (regression verification)

---

## Test Walkthrough Execution

### 1. Pre-Migration Data Population (SQLite)

A rich SQLite store was initialized and populated with diverse entities spanning multiple functional areas:
- **Teams**: Default team (`default`) and secondary team (`core-team`, `infra-team`).
- **Custom Fields**:
  - `priority` (Enum: `["low", "medium", "high"]`, scoped to `default` team)
  - `component` (Text, scoped to `core-team`)
  - `cluster` (Enum: `["us-central1", "europe-west1"]`, scoped to `infra-team`)
- **Templates**: Reusable task template `tmpl-upgrade-verify` ("Upgrade Verification").
- **Schedules**: Daily recurrence schedule `sched-nightly-sync` targeting `bwsalmon/grain` with time of day 180m (03:00 UTC).
- **Tasks & Conversations**:
  - Tasks #1, #2, #3 populated with attributions, repository targets, and directives.
  - Linked task comments added to conversation history for each task.
  - Task run attempts and finished states recorded with outcomes and execution durations.
  - Pre-allocated task ID sequence counter advanced to 3.

### 2. Migration Execution (CLI & REST Routes)

The migration was executed and verified via both CLI tools and REST API interfaces:

#### A. CLI Subcommand Aliases Tested
- `grain db upgrade-sqlite-to-postgres -data-dir /tmp/grain-walkthrough -dsn "postgres://postgres:postgres@localhost:5432/grain_walkthrough_test?sslmode=disable"`
- `grain upgrade-postgres`
- `grain upgrade-sqlite-to-postgres`
- Flags verified: `-data-dir`, `-sqlite-path`, `-dsn`, `-no-data`, `-dry-run`.

**CLI Output Result**:
```
Upgrading database from SQLite (/tmp/grain-walkthrough/store) to PostgreSQL...
Saved database config to /tmp/grain-walkthrough/db-config.json
Successfully upgraded database to PostgreSQL (82 tables migrated in 3.606s)
```

#### B. REST API Route Aliases Tested
- `POST /api/settings/db/migrate-to-postgres`
- `POST /api/database/upgrade`
- `POST /api/upgrade/postgres`
- Endpoints verified with payload validation, JSON responses, error handling, and test-connection verification.

#### C. Configuration Artifact Generated
Saved `/tmp/grain-walkthrough/db-config.json`:
```json
{
  "driver": "postgres",
  "dsn": "postgres://postgres:postgres@localhost:5432/grain_walkthrough_test?sslmode=disable"
}
```

### 3. Data Fidelity & Verification in PostgreSQL

Direct queries against PostgreSQL verified 100% entity and relation fidelity:
- **Table Count**: 82 base tables created and verified.
- **Task Verification**: 3 tasks migrated with exact IDs, titles, bodies, and attribution metadata.
- **Comment Verification**: 3 comments migrated with foreign key links to task records.
- **Template Verification**: Template `tmpl-upgrade-verify` verified with matching name, title, and body.
- **Schedule Verification**: Schedule `sched-nightly-sync` verified with daily recurrence and 180 minute offset.
- **Boolean Integrity**: Boolean flags (`auto_merge`, `enabled`, `auto_approve_proposals`) correctly mapped to native PostgreSQL boolean types.

### 4. Post-Migration Live Mutations & Daemon Verification

The Grain daemon was started in PostgreSQL mode against the migrated database:
```bash
grain daemon -data-dir /tmp/grain-walkthrough -ui-addr 127.0.0.1:8425 -sandbox-dir /tmp/sandboxes
```

Live operations were performed against the active server:
1. `grain -server http://127.0.0.1:8425 list`:
   - Successfully retrieved all 3 migrated tasks.
2. `grain -server http://127.0.0.1:8425 -as alice create -repo bwsalmon/grain -title "Newly Created Task in Postgres"`:
   - Sequence generator allocated Task #4 without ID collision.
3. `grain -server http://127.0.0.1:8425 -as alice comment 4 "Operator live comment on Postgres task"`:
   - Comment record inserted with comment sequence ID #4.
4. `grain -server http://127.0.0.1:8425 get 4`:
   - Retrieved full task metadata, history, and appended conversation comment.

Direct SQL inspection confirmed Task #4 and Comment #4 stored seamlessly in PostgreSQL:
```sql
SELECT id, title FROM task ORDER BY id;
 id |             title              
----+--------------------------------
 1  | SQLite Pre-populated Task #1
 2  | SQLite Pre-populated Task #2
 3  | SQLite Pre-populated Task #3
 4  | Newly Created Task in Postgres

SELECT id, task_id, body FROM task_comment ORDER BY id;
 id | task_id |                  body                  
----+---------+----------------------------------------
  1 | 1       | Initial comment for task #1
  2 | 2       | Initial comment for task #2
  3 | 3       | Initial comment for task #3
  4 | 4       | Operator live comment on Postgres task
```

---

## Issues Discovered & Fixes Applied

| # | Issue Description | Root Cause | Fix Applied |
|---|-------------------|------------|-------------|
| 1 | `ERROR: cannot delete from view "lease_live"` during data import | PostgreSQL `sqlite_master` view definition mapped all objects to `'table'` | Updated `sqlite_master` view definition in `pkg/model/postgres/postgres.go` and `schema.go` to use `CASE WHEN table_type = 'VIEW' THEN 'view' ELSE 'table' END` |
| 2 | `unable to encode 0 into binary format for bool (OID 16)` | SQLite booleans exported as integers (0/1) in JSON; pgx driver rejected `int64` for boolean columns | Added `isBoolColumn` helper in `pkg/staterepo/dump.go` to coerce integer/string booleans to Go `bool` during import |
| 3 | Regex compilation bottleneck during high-concurrency queries | 128 regexes compiled on every query execution in `adaptBoolExpressions` and `adaptDDL` | Precompiled all boolean expression and DDL replacers at package `init()` in `pkg/model/postgres/adapter.go` |
| 4 | `column "multiple" is of type boolean but expression is of type integer` | SQL statements inserting raw integer literals `0`/`1` into boolean columns | Implemented `splitSqlValues` and `adaptInsertBoolLiterals` in `pkg/model/postgres/adapter.go` |
| 5 | `ERROR: duplicate key value violates unique constraint "task_sequence_pkey"` | PostgreSQL sequence objects (`task_sequence_number_seq`, etc.) remained at 1 after bulk row import from SQLite | Added `syncPostgresSequences` in `pkg/model/postgres/upgrade.go` using dynamic schema inspection and `setval()` |
| 6 | `ERROR: column "r.started_at" must appear in the GROUP BY clause` on `grain list` | `TaskSpans` query contained bare `r.started_at` in SELECT list with `GROUP BY t.id` | Added query adapter rule in `adaptSubqueriesAndLimits` for `TaskSpans` to rewrite `MIN("r"."started_at")` and expand `GROUP BY` |

---

## Automated Test Results

All test suites across the repository executed and passed:

```
=== Package Test Summary ===
PASS: github.com/ai-on-gke/grain/pkg/model/postgres       (3.067s - 34 tests)
PASS: github.com/ai-on-gke/grain/pkg/staterepo            (70.732s - 28 tests)
PASS: github.com/ai-on-gke/grain/pkg/ui                   (120.354s - 64 tests)
PASS: github.com/ai-on-gke/grain/cmd/grain                (45.852s - 22 tests)
PASS: github.com/ai-on-gke/grain/pkg/model/sqlite         (34.478s - 31 tests)
PASS: github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest (0.368s)
```

**Walkthrough Integration Test**: `TestPostgresComprehensiveWalkthrough` in `pkg/model/postgres/upgrade_test.go` PASSED in 3.06s.

---

## Conclusion

The PostgreSQL storage engine and SQLite upgrade pipeline are robust, fully verified, and ready for production deployment. All CLI aliases, REST endpoints, data fidelity checks, and sequence generator behaviors operate with zero data loss and full operational compatibility.
