# Test Run Report: Chunked Append-Only Live CI Logs Storage and Streaming API

**Date**: 2026-09-23  
**Status**: Passed  
**Specification**: `docs/test-plans/2026-09-23.md §2`  
**Test Harness**: `cmd/grain/ci_chunked_streaming_walkthrough_test.go`, `pkg/model/sqlite/ci_run_store_test.go`, `pkg/model/postgres/ci_run_store_test.go`  

---

## 1. Executive Summary

This test run validates the end-to-end walkthrough and operational characteristics of Grain's chunked append-only live CI step logs storage engine and streaming API (`get_ci_log` MCP tool and UI `/api/ci/runs/{id}` streaming endpoints).

### Objectives Verified
1. **Live High-Throughput Step Streaming**: Verified live execution of workflows emitting continuous stdout lines over time without blocking the execution engine or database writers.
2. **Concurrent Chunk Creation and Row Contention Avoidance**: Verified that `PutCIStepOutput` appends delta output into `ci_step_log_chunk` records while a step is in flight, avoiding full-row updates and row contention on `ci_step`.
3. **Seamless Progressive Streaming**: Verified concurrent calls to MCP `get_ci_log` using progressive `offset` and `tail` queries during live execution, confirming zero gaps, zero duplicated lines, and correct `still_running` / `complete` indicators.
4. **Post-Completion Consolidation & Cleanup**: Verified that upon run conclusion, all chunks in `ci_step_log_chunk` are atomically cleaned up and the consolidated full output is stored in `ci_step.output`.
5. **Edge Cases & Retention Pruning**: Verified querying past the end of live logs, rollover elision handling, and automated retention sweeping via `PruneCIStepOutput`.
6. **Bug Fix & Regression Prevention**: Fixed rollover non-prefix elision handling in `PutCIStepOutput` (`pkg/model/ci_run_store.go`), adding unit regression test suites in both SQLite and PostgreSQL backends.

---

## 2. Test Environment

- **Host Environment**: Debian GNU/Linux 12 (bookworm), 4 vCPUs, 8 GB RAM.
- **Go Runtime**: Go `1.26.6 linux/amd64`.
- **Node Runtime**: Node.js `v20.18.0`, npm `10.8.2`.
- **Database Engine**: Embedded SQLite with WAL mode (`journal_mode=WAL`, `busy_timeout=5000ms`, `_txlock=immediate`) and PostgreSQL model test suite.
- **Components Tested**:
  - `pkg/model/ci_run_store.go` (`LiveCIChunkStore`, `PutCIStepOutput`, `ci_step_log_chunk`)
  - `pkg/ci/live.go` (`liveOutput`, `LiveOutputInterval`)
  - `pkg/ci/runstore_stored.go` (`StoredRunStore`, `CIStepOutputChange`)
  - `pkg/mcp/grain_ci_wait_tool.go` (`get_ci_log`, `renderCILogRange`, `grainCILog`)
  - `pkg/ui/server.go` (`GET /api/ci/runs/{id}`)

---

## 3. Walkthrough Execution & Coverage Table

| Step / Verification Area | Verification Method | Outcome | Detail |
|---|---|---|---|
| **1. Live CI Workflow Execution** | High-throughput logging step executed in `streamingWalkthroughSandbox` | **Pass** | Emitted 6 sequential log chunks (60 lines, 2,160 bytes) with controlled simulated time advances across `ci.LiveOutputInterval`. |
| **2. Delta Chunk Appending in DB** | Inspection of `ci_step_log_chunk` rows vs `ci_step.output` during live execution | **Pass** | `ci_step_log_chunk` rows scaled incrementally with each published delta (0 -> 1 -> 2 -> 3 -> 4) while `ci_step.output` remained empty base record during streaming. |
| **3. Progressive Offset Streaming** | MCP `get_ci_log` calls with advancing `offset` parameter | **Pass** | Streamed continuous log output chunks matching exact line sequences. Zero gaps, zero duplicate lines, `next_offset` tracking correctly. |
| **4. Live Log Tailing** | MCP `get_ci_log` calls with `tail` parameter | **Pass** | Tailing returned the latest bytes of in-flight output with accurate headers (`still_running: true`). |
| **5. HTTP API Live Visibility** | `GET /api/ci/runs/{id}` during live execution | **Pass** | Returned HTTP 200 with current live run status and concatenated step output from active chunks. |
| **6. Run Conclusion & Consolidation** | Finalization of CI run and database inspection | **Pass** | `ci_step.output` updated to full 2,160 byte output; all temporary `ci_step_log_chunk` rows purged (`COUNT(*) = 0`). |
| **7. Reading Past Log End** | MCP `get_ci_log` with `offset > total_size` | **Pass** | Returned `returned: 0`, `complete: true`, `still_running: false` with zero errors. |
| **8. Retention Pruning** | `store.PruneCIStepOutput` with byte ceiling bound | **Pass** | Swept 1 run, 1 step, 2,160 bytes, setting `ci_step.output` to empty string and purging chunk references. |
| **9. Rollover Elision Handling** | `PutCIStepOutput` non-prefix middle-elision in SQLite & PostgreSQL | **Pass** | Outdated chunks cleared and base output updated when log buffer compacts middle lines. |

---

## 4. Discovered Issues and Fixes

### Rollover / Middle-Elision in Streaming Buffer (`PutCIStepOutput`)
- **Finding**: During high-volume logging steps exceeding `maxStepOutput` (64 KB), `liveOutput` compacts the middle of the buffer and keeps head + tail with an elision notice `\n[... %d bytes omitted ...]\n`. In `PutCIStepOutput`, delta appending assumed new output is always a strict suffix extension (`strings.HasPrefix(output, current)`). When elision occurred, this prefix check failed, resulting in fallback to resetting base output but without deleting outdated chunks from `ci_step_log_chunk`, causing concatenated reads (`ciJobsOf`) to return corrupted/duplicated chunks.
- **Fix Applied**: Updated `PutCIStepOutput` in `pkg/model/ci_run_store.go` to explicitly delete existing chunks (`DELETE FROM ci_step_log_chunk WHERE run_id = ? AND job_index = ? AND step_index = ?`) and update base `ci_step.output = output` whenever non-prefix rollover occurs.
- **Regression Tests**:
  - `pkg/model/sqlite/ci_run_store_test.go`: `TestChunkedAppendOnlyStorageRolloverElision`
  - `pkg/model/postgres/ci_run_store_test.go`: `TestChunkedAppendOnlyStorageRolloverElision`
  - `cmd/grain/ci_chunked_streaming_walkthrough_test.go`: `TestChunkedLiveCIStepLogsStorageAndStreamingWalkthrough`

---

## 5. Verification Test Suite Results

- **`go test ./cmd/grain -run TestChunkedLiveCIStepLogsStorageAndStreamingWalkthrough`**: Passed (0.27s).
- **`go test ./pkg/model/...`**: Passed (SQLite & PostgreSQL tests passed).
- **`go test ./pkg/ci/...`**: Passed.
- **`go test ./pkg/mcp/...`**: Passed.
- **`npm test` in `ui/`**: 134 test files passed, 2,422 tests passed.
- **`go build -o bin/grain ./cmd/grain`**: Passed.
