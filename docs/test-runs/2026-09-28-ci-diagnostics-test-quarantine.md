# Test Run: Advanced CI Diagnostics, Flake Retry & Test Quarantining Verification

**Date**: 2026-09-28  
**Test Plan Reference**: `docs/test-plans/2026-09-28-changes-test-plan.md` §3 Track 4 & §5 ("Advanced CI Diagnostics, Flake Retry & Test Quarantining")  
**Target Branch**: `grain/task-955`  
**Environment**: Debian Linux x86_64, Go 1.26.6, SQLite 3.x / PostgreSQL  

---

## 1. Executive Summary

This test run validates the comprehensive end-to-end verification for **Advanced CI Diagnostics, Flake Retry, and Test Quarantining MCP Tools** in Grain.

The walkthrough confirms:
1. **Test Quarantine Engine (`pkg/ci/quarantine.go`, `.grain/quarantine.yml`)**:
   - Quarantined test definitions support exact matching, package-qualified matching, subtest prefix matching (`TestFoo/Sub1`), wildcard glob matching, and regex patterns with expiration TTL validation.
   - When all failing tests match active quarantine rules (and no compilation/linter diagnostics or unparseable raw errors exist), CI evaluates to non-blocking `ConclusionSuccess` with status `passed (quarantined)` and warning diagnostics, unblocking merge queue progression.
   - Genuine regressions in unquarantined tests continue to strictly yield `ConclusionFailure` and block merges.

2. **Quarantine MCP Tooling (`quarantine_test`)**:
   - Records structured quarantine metadata in `.grain/quarantine.yml` including test name, reason, creation timestamp, author task ID, expiration timestamp (TTL), and linked follow-up task ID.
   - Automatically files a linked follow-up repair task in the task queue targeting test repair and unquarantining.

3. **CI Diagnostic MCP Tools (`get_ci_log`, `retrigger_ci`, `query_test_history`, `run_ci_job`)**:
   - `get_ci_log`: Supports zero-based byte offset/limit slicing and end-of-log tailing (`tail: true` or byte count) across live running and finished CI runs.
   - `retrigger_ci`: Triggers fresh, uncached workflow executions bypassing cached verdicts on demand.
   - `query_test_history`: Aggregates historical executions across workflow runs for specified test identifiers, computing precise pass, failure, and flake rates.
   - `run_ci_job`: Executes fast targeted single-job verification without invoking unrelated workflow jobs and streams job output.

4. **Structured Test Failure Extraction & In-Flight Infrastructure Retry**:
   - `wait_for_checks`: Automatically parses step test output (Go test failures, Playwright, Jest, compiler diagnostics) into structured `FailureSummary` objects displaying failed tests, test locations, failure messages, and diffs.
   - In-flight job retry on infrastructure errors (such as transient TCP timeouts, socket drops, runner host resource contention): automatically re-acquires a fresh sandbox and executes a single retry attempt. When passing on retry, the job is marked `passed (retry)` with flake telemetry recorded.

All unit tests in `pkg/ci` and `pkg/mcp` as well as the comprehensive end-to-end walkthrough in `tests/e2e/mcp_diagnostics_and_quarantine_walkthrough_test.go` passed cleanly.

---

## 2. Test Architecture & Verification Matrix

### A. Test Quarantine Engine & Lifecycle (`pkg/ci/quarantine.go`, `pkg/mcp/quarantine_tool.go`)
- **Quarantine Parsing & TTL**: Validates parsing of `.grain/quarantine.yml`, active vs expired entries (`IsActive`), and duration parsing (e.g., `14d`, `720h`, `30d`).
- **Flexible Pattern Matching**: Verifies exact matching, package-qualified names (`pkg/foo.TestBar`), subtest prefixes (`TestFoo/Subtest1`), glob patterns (`*Flaky*`), and regex compilation.
- **MCP Tool Execution**: Invokes `quarantine_test` with `file_follow_up: true`, asserting yaml file updates and follow-up repair task proposal.
- **Non-Blocking Evaluation**: Verifies that when a CI job fails only on quarantined tests, `ci.Engine` converts the job status to `passed (quarantined)` with `ConclusionSuccess`.
- **Mixed Failure Blocking**: Verifies that when both quarantined and unquarantined tests fail, the build fails with `ConclusionFailure`.

### B. CI Diagnostics Tooling (`pkg/mcp`, `pkg/ci`)
- **`get_ci_log`**: Verifies byte offset and limit positioning, tailing, running job autodetection, and repo boundary enforcement.
- **`query_test_history`**: Queries test execution history across runs, calculating execution counts, pass rates, fail rates, and flake rates.
- **`retrigger_ci`**: Tests launching fresh uncached workflow executions for a specific commit or workflow file.
- **`run_ci_job`**: Tests selective single-job execution and logs return.

### C. Structured Test Failure Extraction & Flake Retry (`pkg/ci/diagnostics.go`, `pkg/ci/job_executor.go`)
- **Structured Failure Parsing**: Verifies extraction of Go test failures, Playwright failures, compiler diagnostics, and raw errors from step output into structured `FailureSummary` format in `wait_for_checks`.
- **In-Flight Infrastructure Retry**: Verifies transient error detection (e.g. `i/o timeout`, `connection refused`), fresh sandbox acquisition (`Acquire`), retry attempt execution, and `passed (retry)` status reporting.

---

## 3. End-to-End Walkthrough Execution Results

### Test Execution: `tests/e2e/mcp_diagnostics_and_quarantine_walkthrough_test.go`

```
=== RUN   TestMCPDiagnosticsAndQuarantineWalkthrough
=== PAUSE TestMCPDiagnosticsAndQuarantineWalkthrough
=== CONT  TestMCPDiagnosticsAndQuarantineWalkthrough
    mcp_diagnostics_and_quarantine_walkthrough_test.go:50: === Section 1: Git Revert Tooling ===
    mcp_diagnostics_and_quarantine_walkthrough_test.go:177: === Section 2: Merge Queue Diagnostics ===
    mcp_diagnostics_and_quarantine_walkthrough_test.go:232: === Section 3: CI Log Retrieval & Tailing ===
    mcp_diagnostics_and_quarantine_walkthrough_test.go:256: === Section 4: Test Quarantine Lifecycle ===
    mcp_diagnostics_and_quarantine_walkthrough_test.go:316: === Section 4B: CI Engine Non-Blocking Execution ===
    mcp_diagnostics_and_quarantine_walkthrough_test.go:374: === Section 4C: Mixed Failure Blocking Verification ===
    mcp_diagnostics_and_quarantine_walkthrough_test.go:404: === Section 5: CI Diagnostic Tools ===
    mcp_diagnostics_and_quarantine_walkthrough_test.go:544: === Section 6: Structured Test Failure Extraction ===
    mcp_diagnostics_and_quarantine_walkthrough_test.go:589: === Section 7: In-Flight Job Retry on Infrastructure Errors ===
--- PASS: TestMCPDiagnosticsAndQuarantineWalkthrough (0.06s)
PASS
ok  	github.com/bwsalmon/grain/tests/e2e	0.076s
```

### Unit Tests: `pkg/ci`, `pkg/mcp`

```
=== RUN   TestAutomaticRetryOnTransientSocketTimeoutPasses
--- PASS: TestAutomaticRetryOnTransientSocketTimeoutPasses (0.00s)
=== RUN   TestAutomaticRetryOnRunnerHostResourceContentionPasses
--- PASS: TestAutomaticRetryOnRunnerHostResourceContentionPasses (0.00s)
=== RUN   TestAutomaticRetryOnInfrastructureStepFailurePasses
--- PASS: TestAutomaticRetryOnInfrastructureStepFailurePasses (0.00s)
=== RUN   TestQuarantineParsingAndMatching
--- PASS: TestQuarantineParsingAndMatching (0.00s)
=== RUN   TestQuarantineEvaluation
--- PASS: TestQuarantineEvaluation (0.00s)
=== RUN   TestQuarantineTool
--- PASS: TestQuarantineTool (0.00s)
=== RUN   TestGetCILog
--- PASS: TestGetCILog (0.00s)
=== RUN   TestRetriggerCI
--- PASS: TestRetriggerCI (0.00s)
=== RUN   TestRunCIJobTool
--- PASS: TestRunCIJobTool (0.00s)
=== RUN   TestTestHistoryExtraction
--- PASS: TestTestHistoryExtraction (0.00s)
PASS
ok  	github.com/bwsalmon/grain/pkg/ci	0.010s
ok  	github.com/bwsalmon/grain/pkg/mcp	0.014s
```

---

## 4. Verification Summary

| Component | Target Area | Validation Details | Verdict |
|---|---|---|---|
| `pkg/ci/quarantine.go` | Test Quarantine Engine | Pattern matching (exact, prefix, glob, regex), TTL calculation, non-blocking CI evaluation | **PASS** |
| `pkg/mcp/quarantine_tool.go` | `quarantine_test` Tool | `.grain/quarantine.yml` persistence, follow-up repair task filing in task queue | **PASS** |
| `pkg/mcp/grain_ci_wait_tool.go` | `get_ci_log` | Offset/limit byte slicing, end tailing (`tail: true`/integer), running job selection | **PASS** |
| `pkg/mcp/grain_ci_wait_tool.go` | `retrigger_ci` | Fresh uncached workflow execution triggering against specified commit | **PASS** |
| `pkg/mcp/grain_ci_wait_tool.go` | `query_test_history` | Historical pass/fail/flake aggregation across remembered runs | **PASS** |
| `pkg/mcp/run_ci_job_tool.go` | `run_ci_job` | Fast targeted single-job execution with streaming log output | **PASS** |
| `pkg/ci/diagnostics.go` | Failure Extraction | Structured parsing of test failures, locations, messages, and compiler diagnostics | **PASS** |
| `pkg/ci/job_executor.go` | Flake Retry | Automatic fresh sandbox acquisition and single retry on transient infrastructure errors | **PASS** |
| `tests/e2e` | End-to-End Walkthrough | Multi-stage integrated walkthrough (`TestMCPDiagnosticsAndQuarantineWalkthrough`) | **PASS** |
