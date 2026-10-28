# MCP propose_task Declarative Parameter Expansion & Templates Test Run

## Build under test

- Repository/branch: `bwsalmon/grain`, `grain/task-544`
- Commit: `95472f974` (`Merge pull request #1998 from bwsalmon/grain/task-549`)
- Environment: Debian 12 amd64 sandbox; Go 1.26.6; Node 20.19.0; SQLite 3.40.1.
- Target plan: `docs/test-plans/2026-09-24.md` §4 ("MCP propose_task Full Parameter Expansion & Declarative Task Instantiation", covering PRs `#1923`, `#1917`, `#1915`, `#1916`, `#1912`).

## Walkthrough Procedure and Assertions

A comprehensive end-to-end verification walkthrough was executed in `tests/e2e/mcp_propose_task_walkthrough_test.go` (`TestMCPProposeTaskWalkthroughSection4`) and `pkg/orchestrator/finish_test.go` (`TestProcessResultFilesProposedTaskWithDeclarativeParametersAndTemplates`), verifying the complete lifecycle of expanded declarative task parameters in `propose_task`, template instantiation with field interpolation, and execution dispatching:

1. **Declarative Parameter Expansion via Live MCP Proposal (`client.CreateTask` / `daemonProposalFiler`)**:
   - Registered reusable parameterized template `tmpl-perf-audit` in Store (`Title: "Performance audit for {{service}}"`, `Body: "Run memory and latency benchmarks for {{service}} under {{load_tier}} load."`, `NetworkAccess: model.NetworkNone`, default `load_tier: "high-throughput"`).
   - Seeded baseline queued tasks to verify prioritized queue placement.
   - Called `propose_task` with full declarative parameters:
     - `template_id: "tmpl-perf-audit"`, `template_fields: {"service": "billing-service"}`
     - `at_front: true`
     - `no_repo: true`
     - `sandbox_cpus: 4`, `sandbox_memory_mb: 8192`, `sandbox_disk_gb: 40`
     - `network_access: "none"`
     - `reads: ["acme/docs"]`
     - `writes: [{"repo": "acme/backend", "base": "release-v2"}]`
     - `capabilities: ["grain-ci"]`
     - `model: "claude-sonnet-5"`, `agent_framework: "claude"`
   - Asserted that the created task properly interpolated the title (`"Performance audit for billing-service"`) and body (`"Run memory and latency benchmarks for billing-service under high-throughput load."`).
   - Verified that `Target` is `nil` for standalone execution (`no_repo: true`).
   - Verified that VM sizing (`4 CPUs, 8192 MB RAM, 40 GB disk`) and `network_access: "none"` were persisted in the SQLite store.
   - Verified that cross-repo `reads` and `writes` with branch target (`acme/backend@release-v2`) and `capabilities: ["grain-ci"]` were correctly resolved and saved.
   - Verified that `at_front: true` placed the task ahead of existing baseline queued tasks in `OrderKey`.

2. **Orchestrator End-of-Run Replay Handling (`pkg/orchestrator/finish.go`)**:
   - Simulated agent task execution producing a `propose_task` tool call with template instantiation (`template_id: "tmpl-perf-audit"`, `template_fields: {"service": "auth-gateway", "load_tier": "extreme"}`), custom VM sizing (`8 CPUs, 16384 MB RAM, 80 GB disk`), `no_repo: true`, `network_access: "none"`, `reads`, `writes`, and `capabilities: ["grain-ci"]`.
   - Processed the run result via `orchestrator.ProcessResult` and verified that `relayProposedTasks` correctly loads the referenced template, performs field value substitution into title and body, extracts VM sizing via `argInt`, and persists model/framework bindings.

3. **Dispatcher Cycle Verification (`dispatch.Cycle`)**:
   - Advanced clock and ran dispatcher cycle against the store.
   - Asserted that the prioritized `at_front` task was dispatched first ahead of older baseline tasks, honoring the calculated `OrderKey` order.

## Test Results and Verification

- Unit test in `pkg/orchestrator`: `go test -v ./pkg/orchestrator -run TestProcessResultFilesProposedTaskWithDeclarativeParametersAndTemplates` -> **PASS** (0.20s).
- End-to-end walkthrough test: `go test -v ./tests/e2e -run TestMCPProposeTaskWalkthroughSection4` -> **PASS** (4.13s).
- Full `pkg/...` test suite: `go test ./pkg/...` -> **PASS** (all 60+ packages passing).
- Full `tests/e2e/...` test suite: `go test ./tests/e2e/...` -> **PASS**.
