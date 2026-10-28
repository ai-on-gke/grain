# Metadata and Model Scalability Limits Test Report

## 1. Executive Summary

This report documents the scalability limits, throughput characteristics, latency profiles, and resource contention boundaries of Grain's metadata and model layers under high scale.

### Test Focus & Objective
As Grain deployments scale to multi-team organizations, higher concurrency of autonomous agents, bursty CI workloads, and frequent human-agent API interactions, the central metadata store and orchestrator reconciler loops must sustain high throughput without unbounded latency growth or database lock starvation.

This evaluation isolates the **metadata, database (SQLite/PostgreSQL), and model code**, excluding physical VM sandbox provisioning, across five core scaling dimensions:
1. **Multi-Team Scalability**: Scaling from 10 to 5,000 distinct teams with per-team backlogs, concurrency limits, and authorization scopes.
2. **Concurrent Agent Execution & Dispatches**: Scaling from 10 to 500 concurrent live agent loops issuing task state transitions, activity heartbeats, capacity evaluations, and run conclusions.
3. **High-Concurrency CI Ingestion & Live Log Streaming**: Ingesting thousands of multi-job CI runs and streaming live step log chunks across dozens of concurrent writers.
4. **High-Throughput API Mutations & Contention**: Simulating heavy human and bot UI/REST API traffic (task creation, comment streaming, state polling, task closures) against the storage engine.
5. **Large Metadata Backlogs & Orchestrator Cycle Scaling**: Measuring `orchestrator.RunCycle` tick latency and individual reconciler durations with backlogs scaling up to 25,000 tasks.

---

## 2. Test Environment & Harness Architecture

- **Host Environment**: Debian GNU/Linux 12 (bookworm), 4 vCPUs, 8 GB RAM, NVMe SSD storage.
- **Go Runtime**: Go `1.26.6 linux/amd64`.
- **Database Engine**: Embedded SQLite with WAL mode (`journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000ms`, `_txlock=immediate`).
- **Test Harness**: `tests/e2e/scalability_test.go` wired into the long test suite (`make scalability` and `.grain/workflows/nightly.yml`). Fast smoke test (`TestScalabilitySmoke`) executed on standard test passes to prevent regression and rot.

---

## 3. Quantitative Results by Scaling Dimension

### Dimension 1: Multi-Team Scalability (10 to 5,000 Teams)

| Metric | 20 Teams | 1,000 Teams | 5,000 Teams | Throughput |
|---|---|---|---|---|
| **Team Creation (`store.PutTeam`)** | 87.5 µs avg | 226.2 µs avg | 264.9 µs avg (p50: 70.2µs, p95: 191.0µs) | **3,751 - 4,392 teams/sec** |
| **`store.ListTeams` Latency** | 137.3 µs avg | 4.87 ms avg (p50: 4.84ms) | 29.89 ms avg (p50: 29.80ms) | $O(N)$ linear scan |
| **`store.ReadyForTeam` Query** | 208.2 µs avg | 184.1 µs avg (p50: 172.4µs) | 195.9 µs avg (p50: 176.2µs) | Sub-millisecond |
| **`store.OrderKeyForNewTaskForTeam`** | 31.3 µs avg | 28.9 µs avg (p50: 25.8µs) | 28.9 µs avg (p50: 25.9µs) | Sub-50µs |

#### Analysis:
- Team creation and point lookups scale with high efficiency (>3,700 ops/sec).
- `ListTeams` scales linearly with the total team count ($~6\mu\text{s}$ per team row scanned). At 5,000 teams, full listing takes ~30ms.
- `ReadyForTeam` maintains sub-millisecond query latency because SQLite optimizes the join between `task_ready` and `task` when filtered by `team`.

---

### Dimension 2: Concurrent Agent Execution (10 to 500 Concurrent Agents)

| Metric | 10 Agents | 250 Agents | 500 Agents | Bottleneck / Note |
|---|---|---|---|---|
| **Aggregate Completed Runs / Sec** | 269.6 runs/sec | 368.5 runs/sec | 256.6 runs/sec | SQLite write transaction serialization |
| **`StartRun` Latency (p50 / p95)** | 415 µs / 12.8 ms | 556 µs / 54.7 ms | 768 µs / 180.7 ms | Capacity checks & team limits scan |
| **`SetTaskActivity` Latency (p50 / p95)** | 106 µs / 3.78 ms | 114 µs / 45.7 ms | 124 µs / 230.0 ms | Lock wait under concurrent writers |
| **`LiveRunCountsForTeam` (p50 / p95)** | 94.8 µs / 2.68 ms | 174.7 µs / 506.8 µs | 288.0 µs / 918.8 µs | Fast indexed count query |
| **`FinishRun` Latency (p50 / p95)** | 82.5 µs / 3.72 ms | 1.19 ms / 530.2 ms | 3.60 ms / 931.2 ms | Transaction commit & state view invalidation |

#### Analysis:
- Under moderate agent concurrency (up to 100-250 concurrent agents), `StartRun` and `FinishRun` maintain p50 latencies under 1.2ms.
- As concurrency increases to 500 parallel agent worker loops, write transaction serialization causes SQLite retry backoffs (`busy_timeout`), inflating tail latencies (p99 reaching ~1.5s - 2.0s).
- Point queries such as `LiveRunCountsForTeam` remain fast (p50: 288µs, p95: 918µs) due to partial index on open runs (`task_run_open_task`).

---

### Dimension 3: Concurrent CI Ingestion & Live Log Streaming

| Metric | 30 Runs (Smoke) | 2,500 Runs | 5,000 Runs | Throughput |
|---|---|---|---|---|
| **`PutCIRun` Ingestion Throughput** | 5,058 runs/sec | 1,881 runs/sec | 1,772 runs/sec | **1,770 - 1,880 runs/sec** |
| **`PutCIRun` Latency (p50 / p95)** | 175.2 µs / 277.9 µs | 149.5 µs / 310.8 µs | 148.9 µs / 309.0 µs | Stable sub-millisecond |
| **`PutCIStepOutput` (50 Concurrent Streams)** | 1,851 updates/sec | 2,028 updates/sec | 1,560 updates/sec | **1,500 - 2,000 log updates/sec** |
| **`PutCIStepOutput` Latency (p50 / p95)** | 108.5 µs / 7.00 ms | 60.2 µs / 135.6 ms | 59.5 µs / 161.1 ms | Lock contention across streams |
| **`ListCIRuns(50)` Query Latency** | 745.5 µs (p50) | 1.08 ms (p50) | 1.08 ms (p50, p95: 2.25ms) | Indexed on `started_at` |

#### Analysis:
- `PutCIRun` exhibits outstanding scalability, sustaining ~1,800 full CI run ingestions per second with p50 latency under 150µs.
- `PutCIStepOutput` achieves ~1,600 to 2,000 updates/sec across 50 concurrent live streaming worker loops.
- `ListCIRuns` query performance is constant-time (~1.08ms p50) even with 5,000+ CI runs stored, validating the effectiveness of the `ci_run_started` index.

---

### Dimension 4: Concurrent API Operations & Mutation Contention

| Metric | 5 API Workers | 50 API Workers | 100 API Workers |
|---|---|---|---|
| **Total Ops / Sec** | 2,960 ops/sec | 1,820 ops/sec | 1,255 ops/sec |
| **Overall API Latency (p50 / p95)** | 254.6 µs / 9.35 ms | 1.11 ms / 60.2 ms | 2.18 ms / 246.1 ms |
| **Task Creation Latency (p50 / p95)** | 1.53 ms / 3.69 ms | 2.97 ms / 260.9 ms | 9.23 ms / 1,087.7 ms |
| **Read States Latency (p50 / p95)** | 679.7 µs / 10.98 ms | 7.73 ms / 34.68 ms | 18.10 ms / 69.08 ms |
| **Update / Comment Latency (p50 / p95)** | 178.8 µs / 9.35 ms | 230.6 µs / 6.45 ms | 230.9 µs / 10.36 ms |

#### Analysis:
- Read-mostly and light mutation operations (`AddComment`, `Close`) remain exceptionally rapid (p50: ~230µs, p95: ~10.4ms) even under 100 concurrent workers.
- Complex state derivation queries (`store.States`) require evaluating the full `task_state` view across thousands of tasks, increasing from 680µs at 50 tasks to 18.1ms at 10,000+ tasks.
- Heavy concurrent task creation under 100 parallel workers generates write transaction serialization queues in SQLite, pushing p95 creation latency to ~1.08s.

---

### Dimension 5: Orchestrator Cycle Scalability Under Scaled Task Backlogs

`RunCycle` tick latency was measured across varying backlog sizes:

| Backlog Size | Total Cycle Duration (p50 / p95) | Dispatch Wait Time (p50 / p95) | Leading Reconciler Bottlenecks |
|---|---|---|---|
| **50 Tasks** | 225.7 ms / 225.7 ms | 9.77 ms / 9.77 ms | `dispatch`: 124.8 ms |
| **10,000 Tasks** | 529.4 ms / 622.0 ms | 125.7 ms / 254.4 ms | `dispatch`: 346.9 ms, `schedule`: 70.4 ms, `reviews`: 25.4 ms |
| **25,000 Tasks** | 772.7 ms / 788.4 ms | 312.9 ms / 320.7 ms | `dispatch`: 430.7 ms, `schedule`: 244.1 ms, `reviews`: 53.7 ms |

#### Reconciler Breakdown at 25,000 Backlog Tasks (Total: 744.5 ms):
- `reconcileDispatch`: **430.7 ms** (57.8% of cycle duration)
- `reconcileSchedules`: **244.1 ms** (32.8% of cycle duration)
- `reconcileReviews`: **53.7 ms** (7.2% of cycle duration)
- `reconcileWake`: **9.1 ms** (1.2% of cycle duration)
- `reconcileSync`: **3.3 ms** (0.4% of cycle duration)
- `reconcileConsoleSessions`: **2.7 ms** (0.4% of cycle duration)
- All other reconcilers (`system-schedules`, `declarations`, `ci`, `branches`, `qualifications`, `suites`): **< 0.1 ms each**

```
Orchestrator Cycle Time Distribution (25,000 Tasks):
┌─────────────────────────────────────────────────────────────┐
│ reconcileDispatch (57.8%)                                   │
├──────────────────────────────────────┬──────────────────────┤
│ reconcileSchedules (32.8%)           │ reconcileReviews (7%)│
└──────────────────────────────────────┴──────────────────────┘
```

---

## 4. Scalability Bottlenecks Identified

1. **Dispatch Candidate Evaluation over Global `task_ready` View**:
   - `reconcileDispatch` currently queries `store.Ready()` which computes `task_ready` across all tasks in the store, and iterates over candidates checking individual team limits and live run counts.
   - At 25,000 tasks, dispatch evaluation takes ~430ms per tick.
2. **Schedule Backlog Scanning in `reconcileSchedules`**:
   - `DueSchedules` and schedule evaluation scales with total task backlog depth, requiring 244ms per tick at 25k tasks.
3. **Missing Secondary Indexes on `task` and `task_run`**:
   - The `task` table currently lacks composite indexes on `(team, order_key)` and `(team)`.
   - `task_run` lacks an index on `(started_at)` and `(task_id, outcome, started_at)`, making the correlated subquery in `task_streak` scale quadratically with long task run histories.
4. **SQLite Single-Writer Lock Contention Under High Agent/API Concurrency**:
   - Above 250 concurrent agents/API workers, SQLite's single-writer limitation introduces retry delays on mutations (`StartRun`, `FinishRun`, `PutTask`), elevating tail latencies (p99 > 1.5s).
   - In contrast, PostgreSQL with connection pooling (`pkg/model/postgres`) handles concurrent multi-connection writes without thread serialization.
5. **Full-String Step Log Updates in `PutCIStepOutput`**:
   - `PutCIStepOutput` updates the entire text column for step output rather than chunking or streaming append-only delta rows, creating write amplification under hundreds of concurrent CI step log streams.

---

## 5. Recommended Follow-Up Tasks

Based on the empirical findings, the following discrete tasks are proposed to improve Grain's metadata and model scalability:

1. **Add covering database indexes for `task` and `task_run`**:
   - Add index `task_team_order` on `task (team, order_key)` to accelerate `ReadyForTeam` and `OrderKeyForNewTaskForTeam`.
   - Add index `task_run_task_outcome_started` on `task_run (task_id, outcome, started_at)` to eliminate quadratic table scans in `task_streak` and `task_state`.
2. **Partition `reconcileDispatch` by active team**:
   - Instead of a monolithic `store.Ready()` scan evaluating all candidates globally, evaluate dispatch per team with queued tasks, leveraging `ReadyForTeam` and per-team worker limits in parallel or targeted batches.
3. **Optimize `task_streak` view and state computation**:
   - Replace correlated subqueries in `task_streak` with indexed window functions or pre-aggregated streak tracking on `task_observation` updated at `FinishRun`.
4. **Chunked append-only storage for CI step logs**:
   - Implement append-only chunked log records for `PutCIStepOutput` to avoid full-text column rewrites during live streaming.
5. **Postgres connection pool & batch write tuning for high concurrency**:
   - Add prepared statements and batch transaction pipelines in `pkg/model/postgres` for deployments running >250 concurrent agents.

---

## 6. Verification and Integration

- **Smoke Suite**: `TestScalabilitySmoke` in `tests/e2e/scalability_test.go` executes across all dimensions in < 1 second.
- **Makefile**: Target `make scalability` added to drive full limits benchmark.
- **Nightly CI**: Added `scalability` job to `.grain/workflows/nightly.yml`.
- All tests pass cleanly under race detector.
