# Test Run: PostgreSQL 64-Bit Duration Metrics, UTF-8 Sanitization & Database Migration

**Date**: 2026-09-30  
**Status**: PASSED (All Automated & End-to-End Walkthrough Tests Verified)  
**Target Branch**: `grain/task-1238`  
**Test Scope**: PostgreSQL 64-bit duration metrics (`BIGINT`), SQLite-to-PostgreSQL upgrade pipeline, UTF-8 query driver parameter sanitization, null byte stripping, live daemon startup with PostgreSQL store, `/api/metrics` accumulation without 32-bit integer overflow, and post-migration task execution.

---

## Executive Summary

As part of the 2026-09-30 test plan (`docs/test-plans/2026-09-30.md §5`), an end-to-end walkthrough was conducted to validate database resilience improvements for large-scale production deployments:
1. **64-Bit Duration Metrics (`BIGINT`)**: Cumulative sandbox execution metrics (`sandbox_usage.duration_ms`) were migrated from 32-bit `INTEGER` to 64-bit `BIGINT` across PostgreSQL schemas and SQLite type affinities. When runtime metrics exceed 2,147,483,647 ms (~596.5 hours), duration accumulation and queries against `/api/metrics` and `/api/sandboxes` continue to aggregate accurately without integer overflow or SQL errors.
2. **UTF-8 & Null Byte Sanitization**: Driver query and argument sanitization (`strings.ToValidUTF8` replacing invalid byte sequences with `\uFFFD` and stripping `\x00` null characters) ensures that non-UTF-8 terminal outputs, binary artifacts, and ANSI noise in CI step outputs or task transcripts migrate cleanly without triggering PostgreSQL `SQLSTATE 22021` (`invalid byte sequence for encoding "UTF8"`).
3. **Database Migration Pipeline**: `grain db upgrade-sqlite-to-postgres` seamlessly translates 87 database tables, recreates schema definitions, executes the `ALTER TABLE IF EXISTS sandbox_usage ALTER COLUMN duration_ms TYPE BIGINT` migration, copies all records with parameter sanitization, and outputs `db-config.json`.
4. **Live PostgreSQL Daemon Operations**: The Grain daemon starts up against PostgreSQL, verifies schema fidelity on startup, serves API and web UI endpoints, correctly reports lifetime sandbox hours (e.g. 1,666.67 total hours from 6,000,000,000 ms), and accepts new task submissions and conversation comments.

---

## Environment & Architecture

- **Host Environment**: Debian 12 (Linux x86_64, 4 vCPU, 8 GB RAM)
- **Database Engine**: PostgreSQL 15.14 (Debian) listening on `127.0.0.1:5432`
- **Source Database Engine**: SQLite 3 (WAL mode)
- **Language / Runtime**: Go 1.24+
- **Key Modules Tested**:
  - `pkg/model/postgres`: Adapter, driver connection pool, SQL dialect translation, schema migrations, and upgrade pipeline.
  - `pkg/model`: `Store`, `SandboxUsage`, `Task`, `Comment`, `CIRun`, and `CIStepOutput`.
  - `pkg/ui`: `/api/metrics`, `/api/sandboxes`, `/api/tasks`, `/api/tasks/{id}/comments`.
  - `cmd/grain`: `grain daemon`, `grain db upgrade-sqlite-to-postgres`, `grain create`.

---

## Detailed Step-by-Step Walkthrough & Verification

### Step 1: PostgreSQL Instance Setup & Health Verification

PostgreSQL 15 was initialized and started on `localhost:5432`. A dedicated test database `grain_walkthrough` was provisioned:

```bash
sudo -u postgres createdb -O postgres grain_walkthrough
```

Connection verification via `psql` succeeded:
```
postgres://postgres:postgres@localhost:5432/grain_walkthrough?sslmode=disable
```

---

### Step 2: Seed SQLite Database with Large Runtime & Non-UTF-8 Data

A pre-migration SQLite store was initialized at `/tmp/grain-pg-walkthrough/store` and seeded with:
- **Large Duration Sandbox Usage (> 2.147 Billion ms)**:
  - Agent Sandbox Kind: `attempted=10, created=10, finished=10, duration_ms=3500000000` (3.5B ms = ~972.22 hours).
  - CI Sandbox Kind: `attempted=20, created=18, finished=18, duration_ms=2500000000` (2.5B ms = ~694.44 hours).
  - Combined Total Duration: 6,000,000,000 ms (~1,666.67 hours).
- **Non-UTF-8 and Null Byte Records**:
  - Task #1: Title containing `\x81\x82` and `\x00`; body containing `\xff\xfe\xfd` and embedded null characters.
  - Task Comment: Body containing raw binary sequence `\x80\x81\x82` and null characters.
  - CI Run & CI Step Output: Log chunk containing high-byte terminal noise `\x81\x92\xff\xfe` and null characters.

---

### Step 3: Execute Migration via CLI (`grain db upgrade-sqlite-to-postgres`)

The migration CLI command was executed against the SQLite store:

```bash
grain db upgrade-sqlite-to-postgres \
  -data-dir /tmp/grain-pg-walkthrough \
  -dsn "postgres://postgres:postgres@localhost:5432/grain_walkthrough?sslmode=disable"
```

**Command Output**:
```
Upgrading database from SQLite (/tmp/grain-pg-walkthrough/store) to PostgreSQL...
Saved database config to /tmp/grain-pg-walkthrough/db-config.json
Successfully upgraded database to PostgreSQL (87 tables migrated in 1.714510777s)
```

Generated `/tmp/grain-pg-walkthrough/db-config.json`:
```json
{
  "driver": "postgres",
  "dsn": "postgres://postgres:postgres@localhost:5432/grain_walkthrough?sslmode=disable"
}
```

---

### Step 4: PostgreSQL Schema & Data Verification

Direct SQL inspections via `psql` confirmed schema migration and data sanitization:

1. **Schema Check (`information_schema.columns`)**:
   ```sql
   SELECT column_name, data_type, udt_name 
   FROM information_schema.columns 
   WHERE table_name = 'sandbox_usage' AND column_name = 'duration_ms';
   ```
   **Output**:
   ```
    column_name | data_type | udt_name 
   -------------+-----------+----------
    duration_ms | bigint    | int8
   (1 row)
   ```

2. **Large Duration Data Check**:
   ```sql
   SELECT kind, attempted, created, finished, duration_ms, since, updated_at FROM sandbox_usage;
   ```
   **Output**:
   ```
    kind  | attempted | created | finished | duration_ms |         since          |       updated_at       
   -------+-----------+---------+----------+-------------+------------------------+------------------------
    agent |        10 |      10 |       10 |  3500000000 | 2026-09-01 00:00:00+00 | 2026-09-30 00:00:00+00
    ci    |        20 |      18 |       18 |  2500000000 | 2026-09-01 00:00:00+00 | 2026-09-30 00:00:00+00
   (2 rows)
   ```

3. **Task & Comment UTF-8 Sanitization**:
   ```sql
   SELECT id, title, body FROM task WHERE id = 'task-walkthrough-1';
   ```
   **Output**:
   ```
            id         |                       title                        |                             body                             
   --------------------+----------------------------------------------------+--------------------------------------------------------------
    task-walkthrough-1 | Task with invalid UTF-8:  and null byte  in title | Body with high byte sequence  and embedded null  characters
   (1 row)
   ```

4. **CI Step Log Sanitization**:
   ```sql
   SELECT run_id, job_index, step_index, chunk_index, output 
   FROM ci_step_log_chunk 
   WHERE run_id = 'run-walkthrough-ci-1';
   ```
   **Output**:
   ```
           run_id        | job_index | step_index | chunk_index |                       output                       
   ----------------------+-----------+------------+-------------+----------------------------------------------------
    run-walkthrough-ci-1 |         0 |          0 |           0 | Build log containing non-utf8  and null  byte end
   (1 row)
   ```

All invalid byte sequences were replaced with `\uFFFD` (``) and all null characters were removed cleanly.

---

### Step 5: Start Grain Daemon on PostgreSQL & Verify Startup

The Grain daemon was launched using the PostgreSQL-configured data directory:

```bash
grain daemon \
  -data-dir /tmp/grain-pg-walkthrough \
  -sandbox-dir /tmp/grain-pg-walkthrough/sandboxes \
  -ui-addr 127.0.0.1:8435 \
  -poll-interval 1s \
  -ci=false
```

Daemon logs confirmed:
- Loading of `db-config.json` pointing to PostgreSQL DSN.
- Startup schema checks and table migrations (including `ALTER TABLE IF EXISTS sandbox_usage ALTER COLUMN duration_ms TYPE BIGINT`).
- Reconciler loop and HTTP server listening on `http://127.0.0.1:8435`.

---

### Step 6: Lifetime Duration Metrics Query Verification (`/api/metrics`)

Endpoints `/api/metrics` and `/api/sandboxes` were queried via HTTP:

```bash
curl -s http://127.0.0.1:8435/api/metrics | jq .sandboxes
```

**JSON Output**:
```json
{
  "total": {
    "attempted": 30,
    "created": 28,
    "failed": 2,
    "finished": 28,
    "unaccounted": 0,
    "seconds": 6000000,
    "hours": 1666.6666666666667,
    "meanSeconds": 214285.7142857143,
    "since": "2026-09-01T00:00:00Z",
    "updatedAt": "2026-09-30T00:00:00Z"
  },
  "kinds": [
    {
      "kind": "agent",
      "attempted": 10,
      "created": 10,
      "failed": 0,
      "finished": 10,
      "unaccounted": 0,
      "seconds": 3500000,
      "hours": 972.2222222222222,
      "meanSeconds": 350000,
      "since": "2026-09-01T00:00:00Z",
      "updatedAt": "2026-09-30T00:00:00Z"
    },
    {
      "kind": "ci",
      "attempted": 20,
      "created": 18,
      "failed": 2,
      "finished": 18,
      "unaccounted": 0,
      "seconds": 2500000,
      "hours": 694.4444444444445,
      "meanSeconds": 138888.88888888888,
      "since": "2026-09-01T00:00:00Z",
      "updatedAt": "2026-09-30T00:00:00Z"
    }
  ]
}
```

**Verification Results**:
- `total.seconds`: 6,000,000 s (exact sum of 3.5M s + 2.5M s).
- `total.hours`: 1,666.67 h.
- `agent.hours`: 972.22 h.
- `ci.hours`: 694.44 h.
- No 32-bit signed integer overflow (which would have capped or wrapped around at negative values if treated as 32-bit integer).
- `/api/sandboxes` lifetime figures perfectly mirror `/api/metrics`.

---

### Step 7: Live Task Dispatch & Post-Migration Mutations

1. **Create Task via REST API**:
   ```bash
   curl -s -X POST http://127.0.0.1:8435/api/tasks \
     -H "Content-Type: application/json" \
     -d '{
       "title": "New Task on PostgreSQL Backend",
       "body": "Validating live task creation, comments, and metrics on PostgreSQL store",
       "team": "default",
       "repo": "bwsalmon/grain"
     }'
   ```
   **Result**: Task #2 created and assigned state `proposed`.

2. **Add Conversation Comment**:
   ```bash
   curl -s -X POST http://127.0.0.1:8435/api/tasks/2/comments \
     -H "Content-Type: application/json" \
     -d '{
       "body": "Live execution comment on PostgreSQL backend with unicode ✔️ test"
     }'
   ```
   **Result**: Comment stored under task #2 in `task_comment` table in PostgreSQL.

3. **Database State Confirmation**:
   ```sql
   SELECT id, title, team, target_owner, target_name FROM task WHERE id = '2';
   SELECT id, task_id, body FROM task_comment WHERE task_id = '2';
   ```
   Confirmed records present with full relational integrity.

---

### Step 8: Automated Regression Test Coverage

Added regression test `TestUpgradeFromSQLiteBigIntDurationAndSanitizationWalkthrough` to `pkg/model/postgres/upgrade_test.go` covering:
- Large duration metrics (> 3.5B ms) migration into `BIGINT`.
- Database schema validation for `sandbox_usage.duration_ms` data type.
- Invalid UTF-8 sequence replacement (`\uFFFD`) and null character stripping across tasks, comments, and CI logs.
- Post-migration delta increments and duration accumulation on PostgreSQL.

Executed and verified:
```bash
go test -v -run TestUpgradeFromSQLiteBigIntDurationAndSanitizationWalkthrough ./pkg/model/postgres/...
```
**Result**: `PASS` (1.75s).

---

## Findings & Resolution Summary

| Item | Finding | Status / Outcome |
|---|---|---|
| **PostgreSQL Duration Type** | `sandbox_usage.duration_ms` successfully declared as `BIGINT` in schema and upgraded on startup via `ALTER TABLE`. | Verified: `bigint` / `int8` column type in PostgreSQL. |
| **32-Bit Overflow Prevention** | Runtimes above 2.147B ms (tested with 3.5B ms and 2.5B ms) calculate and serialize accurately in `/api/metrics`. | Verified: 1,666.67 hours reported cleanly without truncation or overflow. |
| **UTF-8 & Null Byte Sanitization** | `adaptValue` in PostgreSQL adapter strips null characters (`\x00`) and converts non-UTF-8 bytes to `\uFFFD`. | Verified: All raw byte sequences migrated without SQLSTATE 22021 errors. |
| **CLI & Daemon Integration** | `grain db upgrade-sqlite-to-postgres` writes `db-config.json` and `grain daemon` automatically loads PostgreSQL connection. | Verified: 87 tables migrated and live daemon operations successful. |
| **Live Task Operations** | Tasks, comments, and sandbox usages operate seamlessly on migrated PostgreSQL store. | Verified: REST endpoints and direct queries reflect all mutations. |

---

## Conclusion

The PostgreSQL 64-Bit Duration Metrics, UTF-8 Sanitization, and Database Migration enhancements meet all reliability and fidelity criteria specified in `docs/test-plans/2026-09-30.md §5`. All automated tests and live end-to-end walkthroughs pass.
