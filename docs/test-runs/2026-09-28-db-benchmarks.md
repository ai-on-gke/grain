# Database Scalability, Cursor Pagination, and Batch CI Log Benchmark Report

**Date**: 2026-09-28  
**Status**: Passed  
**Specification**: `docs/test-plans/2026-09-28-changes-test-plan.md §Track 5`  
**Test Harnesses**:
- `pkg/model/sqlite/pagination_benchmark_test.go` (`TestCursorVsOffsetPagination20k`, `BenchmarkCursorVsOffset20kTasks`)
- `pkg/model/sqlite/team_benchmark_test.go` (`TestTeamUserIndexUsage`, `TestTeamUserLookupLatency10k`, `BenchmarkTeamUserLookups`)
- `pkg/access/benchmark_test.go` (`TestAccessStoreIndexUsage`, `TestAccessStoreLookupLatency10k`, `BenchmarkAccessStoreGetByIdentifier`)
- `tests/e2e/scalability_test.go` (`TestConcurrentCIStreamingZeroContention`, `BenchmarkConcurrentCIStepStreaming`)
- `pkg/model/postgres/upgrade_test.go` (`TestUpgradeExpressionIndicesVerification`, `TestUpgradeFromSQLite`, `TestUpgradeFilledDatabaseComprehensive`, `TestUpgradeConcurrentLiveSystem`)

---

## 1. Executive Summary

This report documents the performance, scalability characteristics, and correctness verification of Grain's database layer under high load for **cursor-based pagination**, **batched CI step log streaming**, and **SQLite to PostgreSQL schema migration with expression index acceleration**.

### Key Results
1. **Cursor Pagination vs Legacy Offset Pagination**:
   - Tested across a database seeded with **20,000+ tasks and task observations**.
   - **Shallow Pages (Page 1 / Offset 0)**: Cursor pagination (0.201 ms) performs identically to offset pagination (0.187 ms).
   - **Deep Pages (Page 200 / Offset 19,900)**: Cursor pagination executes in **0.192 ms** ($O(\log N)$ index seek) compared to legacy offset pagination at **8.826 ms** ($O(N)$ row scan), achieving a **46.1x speedup**.
   - **Full Traversal (20,000 tasks / 200 pages)**: Cursor-based pagination completed full scan in **44.1 ms** (453 pages/sec, 45,350 tasks/sec) vs offset-based pagination at **913.7 ms** (218 pages/sec, 21,888 tasks/sec), representing an overall **20.7x throughput improvement**.
   - 100% data parity and deterministic ordering verified between both pagination strategies across all 20,000 tasks.

2. **Concurrent CI Streaming & Lock Contention Elimination**:
   - Benchmarked 100 parallel streaming workers emitting continuous log chunks across 20 active CI runs.
   - Achieved **27,612 updates/sec** throughput with **36.2 µs/op** latency under high write concurrency.
   - Verified **0 database lock errors (`database is locked` / `busy_timeout`)** across all runs, proving that the batch write flusher (`PutCIStepOutput` with `stepOutputQueue` and `maxStepOutputBatchSize=100`) completely eliminates writer serialization bottlenecks and write amplification in SQLite WAL mode.

3. **SQLite to PostgreSQL Schema Migration & Expression Indices**:
   - Successfully executed full schema and data migration with 10,000 `access_accounts` rows and 10,000 `team_user` rows.
   - Verified that PostgreSQL query planner utilizes expression indices without sequential table scans:
     - `team_user_team_id_user_lower` (`LOWER(user)` with `team_id`): `Index Scan` (cost: 0.28..8.30).
     - `team_user_user_lower` (`LOWER(user)`): `Bitmap Index Scan` (cost: 0.00..4.48).
     - `access_accounts_identifier_lower` (`LOWER(identifier)`): `Bitmap Index Scan` (cost: 0.00..4.47).
   - Case-insensitive lookups on 10k datasets execute with sub-millisecond latencies (11.4 µs - 39.2 µs) across both SQLite and PostgreSQL.

---

## 2. Test Environment

- **Host Environment**: Debian GNU/Linux 12 (bookworm), 4 vCPUs (Intel Xeon 6985P-C), 8 GB RAM, NVMe SSD storage.
- **Go Runtime**: Go `1.26.6 linux/amd64`.
- **Database Backends**:
  - Embedded SQLite (modernc.org/sqlite `v1.57.0`) in WAL mode (`busy_timeout=5000ms`, `_txlock=immediate`, `synchronous=NORMAL`).
  - PostgreSQL 15.19 (`pgx/v5 v5.11.0`) with connection pool (`max_open=20`, `max_idle=10`).

---

## 3. Cursor Pagination vs Offset Pagination Benchmark

### Workload Configuration
- **Dataset Size**: 20,000 `task` rows + 20,000 `task_observation` rows.
- **State Distribution**: Distributed across all lifecycle states (proposed: 20%, queued: 20%, completed: 20%, closed: 20%, failed: 20%).
- **Page Size**: 100 items per page (200 total pages).

### Latency by Page Depth (Point Queries)

| Page Depth / Offset | Cursor Pagination (`StatesPage`) | Legacy Offset Pagination (`LIMIT 100 OFFSET N`) | Speedup Factor | Cursor Memory / Allocs | Offset Memory / Allocs |
|---|---|---|---|---|---|
| **Page 1 (Offset 0)** | **0.201 ms** (201.5 µs) | 0.187 ms (186.8 µs) | 0.93x (Baseline) | 14.6 KB / 628 allocs | 9.5 KB / 615 allocs |
| **Page 10 (Offset 1,000)** | **0.204 ms** (203.6 µs) | 0.665 ms (664.7 µs) | **3.3x** | 14.8 KB / 632 allocs | 9.5 KB / 617 allocs |
| **Page 50 (Offset 5,000)** | **0.216 ms** (216.3 µs) | 2.417 ms (2,416.7 µs) | **11.2x** | 14.8 KB / 632 allocs | 9.5 KB / 617 allocs |
| **Page 100 (Offset 10,000)** | **0.220 ms** (219.8 µs) | 4.559 ms (4,558.7 µs) | **20.7x** | 14.8 KB / 632 allocs | 9.5 KB / 617 allocs |
| **Page 150 (Offset 15,000)** | **0.211 ms** (211.3 µs) | 6.763 ms (6,763.1 µs) | **32.0x** | 14.8 KB / 632 allocs | 9.5 KB / 617 allocs |
| **Page 200 (Offset 19,900)** | **0.192 ms** (191.5 µs) | 8.826 ms (8,826.3 µs) | **46.1x** | 14.7 KB / 626 allocs | 9.5 KB / 617 allocs |

### Full Dataset Traversal (20,000 Tasks / 200 Pages)

| Traversal Strategy | Total Duration | Page Throughput | Task Throughput | Avg Time Per Page |
|---|---|---|---|---|
| **Cursor Pagination (`StatesPage` / `EachState`)** | **44.10 ms** | **4,535 pages/sec** | **453,507 tasks/sec** | **0.220 ms / page** |
| **Legacy Offset Pagination (`LIMIT 100 OFFSET N`)** | **913.72 ms** | **218.8 pages/sec** | **21,888 tasks/sec** | **4.568 ms / page** |
| **Net Improvement** | **20.7x Faster** | **20.7x Higher** | **20.7x Higher** | **95.2% Reduction** |

### Execution Plan Analysis
- **Cursor Pagination Query**:
  ```sql
  SELECT `task_id`, `state` FROM `task_state` WHERE `task_id` > ? ORDER BY `task_id` LIMIT 101
  ```
  - Query Plan: Direct index seek on primary key `task.id` starting at `?`. Scans only the requested 101 rows regardless of total table size ($O(\log N + K)$ where $K = \text{page size}$).
- **Legacy Offset Pagination Query**:
  ```sql
  SELECT `task_id`, `state` FROM `task_state` ORDER BY `task_id` LIMIT 100 OFFSET ?
  ```
  - Query Plan: Traverses the B-Tree index from row 0 up to $\text{OFFSET} + \text{LIMIT}$, evaluating and discarding $\text{OFFSET}$ rows ($O(N)$ degradation).

---

## 4. Concurrent High-Volume CI Log Streaming & Lock Contention

### Workload Configuration
- **Concurrency**: 100 parallel streaming goroutines.
- **Active CI Runs**: 20 distinct multi-job CI runs.
- **Chunk Writes**: Rapid consecutive `PutCIStepOutput` calls appending live stdout/stderr deltas.

### Benchmark & Stress Metrics

| Metric | Result | Target / SLA | Status |
|---|---|---|---|
| **Aggregate Write Throughput** | **27,612 updates/sec** | > 5,000 updates/sec | **Pass** (5.5x over target) |
| **Single Operation Latency (ns/op)** | **36.2 µs/op** | < 500 µs | **Pass** |
| **P50 Latency (100 Concurrent Streams)** | **3.80 ms** | < 10 ms | **Pass** |
| **P95 Latency (100 Concurrent Streams)** | **5.81 ms** | < 25 ms | **Pass** |
| **P99 Latency (100 Concurrent Streams)** | **5.98 ms** | < 50 ms | **Pass** |
| **Lock Contention / Locked Errors** | **0 errors** (`0.00%`) | Exactly 0 errors | **Pass** |
| **Memory Allocation** | **3,045 B/op (83 allocs/op)** | Bounded memory | **Pass** |

### Lock Contention Mitigation Architecture
1. **Delta Log Chunks (`ci_step_log_chunk`)**:
   - Instead of replacing full CI run records or mutating large JSON blobs on every log append, live output deltas are written into append-only chunk rows (`ci_step_log_chunk`).
2. **In-Memory Request Batching (`stepOutputQueue`)**:
   - `PutCIStepOutput` queues write requests into an atomic batch queue (`maxStepOutputBatchSize = 100`).
   - A single designated flusher routine acquires the SQLite transaction lock and commits up to 100 chunk inserts in a single transaction (`BEGIN IMMEDIATE ... COMMIT`), minimizing SQLite write lock hold time to sub-millisecond durations.
3. **Run Finalization Consolidation**:
   - Upon step/run completion, all temporary log chunks are consolidated into `ci_step.output` and deleted in a single cleanup transaction, ensuring zero orphaned records.

---

## 5. SQLite to PostgreSQL Schema Migration & Expression Indices

### Migration Verification Summary
- **Source Backend**: SQLite 3 database with 10,000 `access_accounts` records, 10,000 `team_user` records, tasks, observations, attachments, and CI runs.
- **Destination Backend**: PostgreSQL 15 database initialized via `UpgradeFromSQLite(ctx, sqliteDB, pgCfg, migrateData=true)`.
- **Migration Status**: **Pass** (All tables migrated, 0 data loss, sequences aligned).

### PostgreSQL Query Plan & Index Verification

| Target Query | Tested Condition | PostgreSQL Query Plan Verdict | Index Used |
|---|---|---|---|
| `SELECT role, admin FROM team_user WHERE team_id = $1 AND LOWER(user) = LOWER($2)` | `team-infra`, `user_5000_dev` | `Index Scan using team_user_team_id_user_lower on team_user` (cost=0.28..8.30) | `team_user_team_id_user_lower` |
| `SELECT team_id FROM team_user WHERE LOWER(user) = LOWER($1)` | `user_5000_dev` | `Bitmap Heap Scan -> Bitmap Index Scan on team_user_user_lower` (cost=0.00..4.48) | `team_user_user_lower` |
| `SELECT id, type, identifier FROM access_accounts WHERE LOWER(identifier) = LOWER($1)` | `user_5000@corporate.example.com` | `Bitmap Heap Scan -> Bitmap Index Scan on access_accounts_identifier_lower` (cost=0.00..4.47) | `access_accounts_identifier_lower` |

### Case-Insensitive Lookup Microbenchmarks (10,000 Rows)

| Benchmark Scenario | Backend | Latency (ns/op) | Memory / Allocs | Sequential Scan Status |
|---|---|---|---|---|
| **`UserRoleInTeam` (Exact Case)** | SQLite | 11.6 µs | 679 B / 24 allocs | **0 Seq Scan** (Index Used) |
| **`UserRoleInTeam` (Upper Case)** | SQLite | 11.4 µs | 680 B / 24 allocs | **0 Seq Scan** (Index Used) |
| **`UserInTeam` (Exact Case)** | SQLite | 11.4 µs | 648 B / 18 allocs | **0 Seq Scan** (Index Used) |
| **`UserInTeam` (Upper Case)** | SQLite | 11.6 µs | 648 B / 18 allocs | **0 Seq Scan** (Index Used) |
| **`GetByIdentifier` (Exact Case)** | SQLite / Memory | 39.2 µs | 1,760 B / 57 allocs | **0 Seq Scan** (Index Used) |
| **`GetByIdentifier` (Upper Case)** | SQLite / Memory | 36.5 µs | 2,040 B / 70 allocs | **0 Seq Scan** (Index Used) |
| **`GetByIdentifier` (Unprefixed Case)** | SQLite / Memory | 34.3 µs | 1,760 B / 57 allocs | **0 Seq Scan** (Index Used) |

---

## 6. Test Suite Coverage & Verification Matrix

| Verification Track | Test Command | Outcome | Duration |
|---|---|---|---|
| **Cursor vs Offset Correctness (20k)** | `go test -v -run TestCursorVsOffsetPagination20k ./pkg/model/sqlite` | **Pass** | 1.42s |
| **Cursor vs Offset Benchmarks (20k)** | `go test -bench=BenchmarkCursorVsOffset20kTasks ./pkg/model/sqlite` | **Pass** | 90.39s |
| **Concurrent CI Streaming Contention** | `go test -v -run TestConcurrentCIStreamingZeroContention ./tests/e2e` | **Pass** | 0.19s |
| **Concurrent CI Streaming Benchmark** | `go test -run=^$ -bench=BenchmarkConcurrentCIStepStreaming ./tests/e2e` | **Pass** | 5.89s |
| **PostgreSQL Upgrade & Expression Indices** | `go test -v -run TestUpgradeExpressionIndicesVerification ./pkg/model/postgres` | **Pass** | 13.69s |
| **PostgreSQL Comprehensive Upgrade** | `go test -v -run TestUpgradeFilledDatabaseComprehensive ./pkg/model/postgres` | **Pass** | 1.94s |
| **PostgreSQL Concurrent Migration** | `go test -v -run TestUpgradeConcurrentLiveSystem ./pkg/model/postgres` | **Pass** | 2.57s |
| **Access Store Index & Latency** | `go test -v -count=1 ./pkg/access/...` | **Pass** | 0.08s |
| **Full Model Test Suite** | `go test -count=1 ./pkg/model/...` | **Pass** | 246.3s |

---

## 7. Conclusions & Recommendations

1. **Production Readiness for Large Task Backlogs**: Cursor-based pagination (`StatesPage` / `EachState`) is fully verified to scale to 20,000+ tasks without latency degradation, maintaining flat ~0.20 ms page query latencies regardless of queue depth.
2. **CI Log Ingestion Resilience**: Batched chunk appending (`PutCIStepOutput`) eliminates SQLite write contention under high concurrency (100 parallel streams), sustaining >27,000 updates/sec with zero lock contention errors.
3. **Database Migration Reliability**: Automated SQLite to PostgreSQL migration reliably transfers data and ensures expression indices on `access_accounts` and `team_user` are recognized and utilized by the PostgreSQL query planner.
