# Per-PR (`tests.yml`) and Nightly (`nightly.yml`) Test Suite Audit

## 1. Executive Summary & Architectural Alignment

Over the past week, Grain's core architecture evolved significantly across seven subsystems:

1. **Repository Split & Retirement of `bwsalmon/grain`:** Core open-source development moved to `ai-on-gke/grain` (`model.GrainSourceRepo()`) alongside 3-segment host-qualified repositories (such as `depot.code.forge.test/experimental/grain`).
2. **State Repository Narrowing (`staterepo.ExportConfig`):** `grain/config` was narrowed (`grain/task-395`) to export configuration and memory tables only (`config`, `repo_config`, `credential_profile`, `task_template`, `review_template`, `secret_metadata`, `memory`), while tasks, runs, comments, observations, and CI steps live exclusively in the database.
3. **Immediate Merge & Path-Overlap Recheck:** Merge batching and bisection were removed (`grain/task-1023`) in favor of immediate merge on clean head, path-overlap recheck (`pkg/orchestrator/mergerecheck.go`), and path-scoped `.grain/merge-rules.yml` policies (`pkg/forge/merge_rules.go`).
4. **Unified In-Sandbox `grain` CLI:** `cmd/grain-sandbox` (`pkg/sandboxcli`) communicating over the loopback sandbox HTTP proxy (`127.0.0.1:9728`, `pkg/daemon/sandbox_cli_proxy.go`) replaced legacy host-side MCP inspection/action tools.
5. **Interactive Sessions:** Persistent chat and director sessions (`chat` tool, `addendaPoller`, `deliveredCursor`, warm session sandboxes in `pkg/orchestrator/session_sandboxes.go`, and `keep_running` / `GoalPersonCloses` tasks).
6. **Detached `runhost` Execution:** Agent runs and CI jobs survive daemon restarts and self-upgrades via `pkg/daemon/runhostupgrade.go` and `pkg/orchestrator/adopt.go`.
7. **GitHub Actions CI & `ci_mode: github`:** Per-PR (`tests.yml`) and scheduled (`nightly.yml`) workflows run on both Grain CI (`.grain/workflows/`) and GitHub Actions (`.github/workflows/`), with webhook/polling ingestion (`pkg/ghwebhook`, `pkg/orchestrator/githubci.go`) and `ci_mode: github` gating (`pkg/forge/observe.go`).

This document records the complete inventory of jobs and test packages across `.grain/workflows/tests.yml`, `.github/workflows/tests.yml`, `.grain/workflows/nightly.yml`, and `.github/workflows/nightly.yml`, the obsolete/duplicate tests pruned during this audit, and the new unit/integration tests added for previously uncovered paths.

---

## 2. Inventory of Workflows, Jobs, and Test Packages

### 2.1 Per-PR Suite (`.grain/workflows/tests.yml` & `.github/workflows/tests.yml`)

Every pull request and `main` push runs the per-PR suite. Invariants in `tests/deploy/grain_workflows_test.go` (`TestTheGoJobsCoverEveryPackageInTheModuleOnce`, `TestTheGrainGoTestStepsStreamProgressThroughRunGoTests`, `TestSelfWorkflowsAskOnlyForWhatTheyUse`) enforce that the five Go jobs partition `go list ./...` without gaps or overlaps, run with `-race`, and declare minimal capabilities.

| Job ID | Display Name | `.grain/workflows/tests.yml` | `.github/workflows/tests.yml` | Packages / Scope | Timeout (Grain / GHA) |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `go` | `go` | Yes | Yes | `make fmt`, `make frontend`, `go build ./...`, `go vet ./...`, and `go test -race ./pkg/ui/... ./cmd/grain/... ./ui/e2e/...` | 25m / 30m |
| `go-e2e` | `go e2e` | Yes | Yes | `make frontend` + `go test -race ./tests/e2e/... ./tests/deploy/...` | 25m / 30m |
| `go-orchestrator` | `go orchestrator` | Yes | Yes | `go test -race ./pkg/orchestrator/...` | 20m / 30m |
| `go-model` | `go model` | Yes | Yes | `go test -race ./pkg/model/... ./pkg/staterepo/... ./tests/security/...` | 20m / 30m |
| `go-unit` | `go unit` | Yes | Yes | `go test -race` across all remaining packages (`./cmd/checkpost/...`, `./cmd/ghcr-prune/...`, `./cmd/github-webhook/...`, `./cmd/grain-connect/...`, `./cmd/grain-sandbox/...`, `./cmd/grit/...`, `./docs/releases/...`, `./tests/hygiene/...`, `./tests/container/...`, `./tests/installer/...`, `./tests/bench/...`, and 58 `./pkg/...` packages including `daemon`, `forge`, `ghwebhook`, `gitproxy`, `mcp`, `namespace`, `runhost`, `sandboxcli`, `trustboundary`, `upgrade`) | 20m / 30m |
| `granule` | `granule` | Yes | Yes | `go test -race ./...` inside `third_party/granule` (`-mod=vendor`) | 15m / 15m |
| `postgres` | `postgres` | Yes | Yes | `GRAIN_REQUIRE_POSTGRES=1` `go test -count=1 ./pkg/model/postgres/... ./pkg/model/` against real PostgreSQL | 15m / 20m |
| `run-host` | `run host` | Yes | Yes | `GRAIN_RUNHOST_DOCKER=1` `go test -race -count=1 ./pkg/runhost/` + `TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest` in `./pkg/cli/` | 15m / 15m |
| `host-script-container` | `host-script container` | Yes | Yes | Builds `Dockerfile.k8s-sandbox` and runs `TestHostScriptGrantInARealContainerSandbox` in `./pkg/orchestrator/` | 15m / 15m |
| `frontend` | `frontend` | Yes | Yes | `make frontend`, `make fmt-frontend`, and `cd ui && npm test -- --maxWorkers=4` (Jest unit tests) | 15m / 15m |
| `ui-e2e` | `ui e2e` | Yes | Yes | `make frontend`, `make playwright-browsers`, and `make test-e2e-suite` (Playwright E2E suite in `ui/e2e/`) | 25m / 35m |
| `lint` | `lint` | Yes | Yes | `shellcheck` over tracked shell scripts (`scripts/*.sh`, `deploy/terraform/*.sh`) | 15m / 15m |
| `terraform` | `terraform` | Yes | Yes | `terraform fmt -check`, `init -backend=false`, and `validate` across `deploy/terraform/gcp` | 15m / 15m |
| `installer-e2e` | `installer` | Yes | Yes | Builds `grain-installer:test` (`scripts/installer-test-image.sh`) and runs `GRAIN_INSTALLER_E2E=1 go test ./tests/installer -count=1 -v` | 15m / 20m |
| `package-cache` | `package cache` | Yes | Yes | Real-dockerd package cache tests (`GRAIN_REAL_DOCKER_CACHE_TESTS=1` in `./pkg/orchestrator/`) + transparent interception test (`GRAIN_REAL_INTERCEPT_TESTS=1` in `./pkg/daemon/`) | 15m / 20m |
| `real-vm` | `real vm` | Moved to `nightly.yml` (`kvm-sandboxes`) | Gated (`vars.ENABLE_SELF_HOSTED_TESTS == 'true'`) | Real Firecracker / Cloud-Hypervisor VM tests in `./pkg/orchestrator/` | — / 30m |
| `real-kubernetes` | `real kubernetes` | — | Gated (`vars.ENABLE_SELF_HOSTED_TESTS == 'true'`) | `kind` + Calico cluster pod sandbox E2E tests (`GRAIN_REAL_K8S_TESTS=1`) in `./pkg/orchestrator/` | — / 30m |

### 2.2 Nightly Suite (`.grain/workflows/nightly.yml` & `.github/workflows/nightly.yml`)

The nightly suite runs on schedule (`cron: "11 2 * * *"` UTC) and via `workflow_dispatch`. `tests/deploy/grain_workflows_test.go` (`TestEverythingTheGateDoesNotRunTheNightlyDoes`, `TestTheNightlySuiteRunsTheLongTargets`, `TestThisRepositoryScheduledWorkflowsFitInsideTheirOwnCadence`) enforces that every long/stress target excluded from `tests.yml` is exercised in `nightly.yml` within the schedule cadence.

| Job ID | Display Name | `.grain/workflows/nightly.yml` | `.github/workflows/nightly.yml` | Target / Scope | Timeout (Grain / GHA) |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `go-long` | `go, shuffled and repeated` | Yes | Yes (4 matrix shards) | `make test-long` (`go test -race -shuffle=on -count=2 ./...`) catching order-dependent and repeat-invocation bugs | 60m / 90m per shard |
| `staterepo-soak` | `state repository soak` | Yes | Yes | `make soak` (`TestStateRepoSoak100Commits` in `./pkg/staterepo/...`) | 35m / 35m |
| `loadtest` | `sustained load` | Yes | Yes | `make loadtest` (`TestHighConcurrencyLoad` in `./tests/e2e/...`) | 35m / 35m |
| `scalability` | `metadata and model scalability` | Yes | Yes | `make scalability` (`TestScalability50Agents` & `TestLargeScaleHistory` in `./tests/e2e/...`) | 35m / 35m |
| `merge-stress` | `merge liveness stress` | Yes | Yes | `make merge-stress` (`TestMergeQueueStress` in `./tests/e2e/...`) | 60m / 60m |
| `long-term-test` | `end-to-end long term tests` | Yes | Yes | `make long-term-test` (`TestLongTermEndToEndSustained` in `./tests/e2e/...`) | 60m / 60m |
| `long-sim` | `long randomized cluster simulation` | Yes | Yes | `make long-sim` (`TestRandomizedClusterLong` in `./tests/e2e/...`) | 45m / 45m |
| `postgres-stress` | `postgres concurrency and stress` | Yes | Yes | `GRAIN_REQUIRE_POSTGRES=1 make postgres-stress` (`./pkg/model/postgres/... -run TestPostgres`) | 20m / 25m |
| `tunnel-stress` | `reverse tunnel multiplexer stress` | Yes | Yes | `make tunnel-stress` (`./pkg/tunnel/...` 30-minute stress suite) | 45m / 45m |
| `ui-e2e-long` | `ui e2e, repeated` | Yes | Yes (3 matrix passes) | `make test-e2e-long` (3 full passes of Playwright E2E suite) | 70m / 60m per pass |
| `installer-variants` | `installer trixie and hostmode` | Yes | Yes | Assembles `grain-installer:test` (`debian:trixie`) and runs `./tests/installer/trixie` + `./tests/installer/hostmode` | 40m / 45m |
| `vulns` | `govulncheck` | Yes | Yes | `make vuln` (`govulncheck ./...` under `go.mod` toolchain) | 10m / 15m |
| `cmd-grain-stress` | `pkg/cli stress under load` | Yes (`paths:` scoped) | Yes | 4-way parallel `go test -race -count=1 ./pkg/cli` rounds within a 1,800s budget | 35m / 40m |
| `kvm-sandboxes` | `real KVM sandboxes` | Yes (`paths:` scoped) | Gated (`vars.ENABLE_SELF_HOSTED_TESTS == 'true'`) | Resolves guest image (`scripts/granule-guest-resolve.sh`) and runs real KVM VM tests in `./pkg/orchestrator/` | 50m / 50m |
| `kvm-route-stress` | `twenty two-VM boots` | Yes (`paths:` scoped) | Gated (`vars.ENABLE_SELF_HOSTED_TESTS == 'true'`) | 20 consecutive boots of `TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently` | 70m / 70m |
| `upgrade-test` | `upgrade from previous version` | Yes (`paths:` scoped) | Yes | Builds `grain-e2e:test` (`scripts/ci-docker-build.sh`) and runs `make test-upgrade` (`./tests/container/ -run 'TestUpgradeFromPreviousVersion\|TestAnUpgrade'`) | 30m / 40m |

### 2.3 Package Inventory Across `tests/...`

- **`tests/e2e/`:** Full-stack in-process daemon + `githubsim` E2E tests (`e2e_test.go`, `lifecycle_test.go`, `merge_queue_test.go`, `schedules_test.go`, `self_host_test.go`, `webhook_test.go`), plus `-short`-skipped nightly stress suites (`load_test.go`, `scalability_test.go`, `merge_stress_test.go`, `long_term_test.go`, `long_sim_test.go`). Note: `tests/sim/` was previously consolidated into `tests/e2e/long_sim_test.go` and `pkg/github/githubsim/`.
- **`tests/deploy/`:** Static and structural invariants over `.grain/workflows/*.yml`, `.github/workflows/*.yml`, `Makefile`, `Dockerfile*`, `Granulefile*`, Terraform modules, and systemd units (`grain_workflows_test.go`, `deploy_test.go`).
- **`tests/installer/`:** Unit tests (`installer_unit_test.go` in `go-unit`), default bookworm E2E installer qualification (`installer_e2e_test.go` in `installer-e2e`), and nightly variant suites (`tests/installer/trixie`, `tests/installer/hostmode` in `installer-variants`).
- **`tests/security/`:** Route authorization, CSRF, capability boundary, gitproxy scope isolation, and secret redaction tests (`go-model` on every PR and `go-long` in nightly).
- **`tests/hygiene/`:** Repository boundary checks (`boundary_test.go` enforcing zero internal tokens in open-source core, license headers, and module tidiness in `go-unit`).
- **`tests/container/`:** Container image and cross-version upgrade verification (`upgrade-test` in nightly and unit checks in `go-unit`).

### 2.4 Division of Responsibilities (`tests.yml` vs `nightly.yml`)

- **`tests.yml` (Per-PR Gate):** Fast, deterministic, single-pass (`-race`) functional and security verification across every Go package, Jest unit suite, Playwright E2E suite, installer qualification, and Terraform/shell/workflow linting. No multi-pass soak loops or unasserted cost-measurement loops belong in `tests.yml`.
- **`nightly.yml` (Scheduled Deep Verification):** Multi-pass order/race fuzzing (`go-long` with `-race -shuffle=on -count=2` and `ui-e2e-long` 3x), long-running stress/soak/simulation suites (`staterepo-soak`, `loadtest`, `scalability`, `merge-stress`, `long-term-test`, `long-sim`, `tunnel-stress`, `cmd-grain-stress`), database/distro/upgrade matrices (`postgres-stress`, `installer-variants`, `upgrade-test`), vulnerability scanning (`vulns`), and bare-metal KVM suites (`kvm-sandboxes`, `kvm-route-stress`).

---

## 3. Pruned, Consolidated, and Modernized Tests

### 3.1 Removed Obsolete State-Repo Churn & Zero-Assertion Cost Measurement Files

1. **`pkg/staterepo/growth_test.go` (Deleted):**
   - `TestStateRepoGrowth` was written prior to `grain/task-395` to measure git object growth when `task_run` and `task_observation` rows were exported to the state repository. Following `grain/task-395`, `staterepo.ExportConfig` exports only configuration and memory tables, making the 6-pass `task_run`/`task_observation` loop a no-op against `grain/config` while still paying git commit/gc overhead on every PR.
   - `pkg/staterepo/config_only_test.go` (`TestExportConfigLeavesOperationalRowsOnlyInSQLite`) and `pkg/staterepo/soak_test.go` already directly verify `ExportConfig` invariants.
2. **`pkg/staterepo/cigrowth_test.go` (Deleted):**
   - `TestCIRunStorageCost` simulated 3 CI runs × 4 jobs × 6 steps (72 step logs) and ran 6 full `staterepo.Export` + `git count-objects` passes on every PR while explicitly stating `// and it asserts nothing`.
   - `TestCIStateRepoGrowth` asserted the absence of `tables/ci_step.json` after 3 multi-job CI runs, duplicating `TestExportConfigLeavesOperationalRowsOnlyInSQLite` in `pkg/staterepo/config_only_test.go`.
3. **`pkg/ci/runstore_live_cost_test.go` (Deleted):**
   - `TestLiveOutputWriteCost` executed 288 SQLite read/write operations across simulated 64 KiB step logs on every PR while explicitly stating `// It asserts nothing`.
   - The incremental step-log write invariant (`SaveLiveOutput` writing only the changed step's blob without rewriting the whole run) is already asserted by `TestALiveStepsOutputIsWrittenWithoutRewritingTheWholeRun` and `TestAPublicationThatMovesWhatOtherStepsKeepIsWrittenWhole` in `pkg/ci/runstore_stored_test.go`.

### 3.2 Consolidated Duplicate `security-audit` Job out of `nightly.yml`

- Removed the standalone `security-audit` job (`make security`, which runs `go vet ./...` + `go test -race -count=1 ./tests/security/...`) from `.grain/workflows/nightly.yml`, `.github/workflows/nightly.yml`, and the nightly table in `docs/github-actions-ci.md`.
- **Why:** As documented in `tests/security/doc.go`, `tests/security` is an ordinary package in the main module without build tags. Every PR already runs `go vet ./...` (in `go`) and `go test -race ./tests/security/...` (in `go-model`), and `nightly.yml`'s own `go-long` job (`make test-long`) already runs `./tests/security/...` twice with `-race -shuffle=on -count=2`.

### 3.3 Modernized Hardcoded `bwsalmon/grain` References & Fixed GHA Failure Annotations

- Updated `tests/deploy/grain_workflows_test.go` (`TestTheGuestCacheJobIsWhatAPostMergeDispatchPlans` and `TestTheBaseBranchGateForThisRepositoryRunsItsTests`) to use `model.GrainSourceRepo().String()` (`"ai-on-gke/grain"`) instead of hardcoded `"bwsalmon/grain"`.
- Updated `pkg/sandboxcli/help.go` and `pkg/sandboxcli/sandboxcli_test.go` CLI examples and test fixtures from `bwsalmon/grain` to `ai-on-gke/grain`.
- Updated `go-unit` and `go-orchestrator` in `.github/workflows/tests.yml` to parse the structured `=== failing tests ===` section emitted by `scripts/run-go-tests.sh` (matching `go-e2e` and `go-model`) instead of grepping `_test\.go:[0-9]+:` from the tail of `/tmp/go-unit.log`, where verbose `t.Log` output in passing tests (`pkg/upgrade/log_test.go:103`) previously masked actual test failures in GitHub Actions `::error` annotations and cross-branch failure attribution.

---

## 4. Coverage Gaps Audited & New Tests Added

### 4.1 Interactive Sessions (`chat`, `addendaPoller`, `deliveredCursor`, Warm Sandboxes)

- **Existing coverage verified:**
  - `pkg/orchestrator/consolesessions_inprocess_test.go`: End-to-end interactive console sessions (`GoalPersonCloses`), warm sandbox reuse across turns, idle TTL expiry (`TestConsoleSessionReclaimsIdleSandboxAfterTTL`), explicit `recreate_sandbox` during a turn, and `addendaPoller` stop-on-run-completion.
  - `pkg/daemon/mcpserver_delivered_test.go`: Single-message `chat` turn advancing `deliveredCursor` and filtering already-delivered comments from `[NEW COMMENTS DURING RUN]` addenda (`TestAChatReplyIsDeliveredOnceAndReported`).
- **Gaps closed in this audit:**
  1. **Max-warm-pool LRU eviction (`evictOldestWarmSessionSandboxes`):** Added `TestEvictOldestWarmSessionSandboxesEvictsInIdleOrderAndMarksReclaimed` in `pkg/orchestrator/consolesessions_inprocess_internal_test.go`, verifying that when the warm session pool exceeds `maxWarmSessionSandboxes`, sandboxes are evicted in oldest-`idleSince` order, destroyed on the host, and marked `sandbox_reclaimed_at` in the store while newer warm sandboxes and active in-flight sandboxes (`inUse: true`) are preserved.
  2. **Direct `addendaPoller` max-ID cursor advancement & out-of-order comment resilience:** Added `TestAddendaPollerUsesMaxSeenIDAndStreamsNewCommentsOnce` in `pkg/orchestrator/consolesessions_inprocess_internal_test.go`, verifying that `startAddendaPoller` initializes `seenID` to the maximum ID across `initialComments` (even when unsorted), streams newly posted user comments once via `DeliverAddendum`, ignores agent-authored comments, and advances `seenID` past `deliveredCursor()`.
  3. **Multi-message burst delivery during a single `chat` turn:** Added `TestMultiMessageChatTurnAdvancesCursorToMaxIDAndFiltersAllFromAddenda` in `pkg/daemon/mcpserver_delivered_test.go`, verifying that when multiple user messages arrive before `chat` unblocks, all burst messages are returned together, `DeliveredUpTo()` advances to the highest message ID, none of those messages leak into subsequent `addendaPoller` ticks, and a later follow-up comment is still delivered normally.
  4. **Playwright Director session restart synchronization (`ui/e2e/director.spec.js`):** Hardened `follows a live Director task through its durable exchange` to wait for `<TaskChat key={task.id}>` in `ConsoleSessionPage` to finish remounting onto the fresh replacement task (`Diagnostic exchange complete.` cleared and `.cs-message` count reset to 2) after clicking `Close this chat and start a fresh one` before typing the next message, preventing `send()` from racing the async `refreshSessionSaved()` remount under CI load.

### 4.2 In-Sandbox `grain` CLI (`cmd/grain-sandbox`, `pkg/sandboxcli`) & Proxy Endpoints

- **Existing coverage verified:**
  - `pkg/sandboxcli/sandboxcli_test.go`, `actions_test.go`, `task_list_test.go`: `grain ls/read/grep`, `grain status`, `grain handover`, `grain task read/list/transcript`, `grain ci list/log/wait/run-job/retrigger/test-history`, `grain pr status/base-status/open/reply/resolve/review-comment`.
  - `pkg/daemon/sandbox_cli_proxy_test.go`: Loopback HTTP proxy authentication, scope enforcement, and handler routing.
- **Gaps closed in this audit:**
  1. **Session & stall task list flags (`--kind`, `--stalled`, `--changed-since`):** Extended `TestSandboxTaskListFiltersAndBounds` in `pkg/sandboxcli/task_list_test.go` to verify `grain task list --kind session`, `--stalled`, and `--changed-since` query parameter forwarding and response formatting.
  2. **`grain api` input modes (`routes`, `--field`, `--file`, stdin) & `grain ci artifact` downloads:** Added `TestSandboxAPIInputModesAndCIArtifactDownload` in `pkg/sandboxcli/actions_test.go`, covering `grain api routes --scope` / `--json`, `--field` JSON literal coercion (booleans, numbers, arrays, objects vs plain strings), `--file <path>` and `--file -` (stdin), and `grain ci artifact` streaming to stdout, writing to `-o <path>`, and cleaning up partial output files when an artifact download fails mid-stream.

### 4.3 Detached `runhost` Adoption Across Daemon Restarts

- **Existing coverage verified:**
  - `pkg/orchestrator/adopt_test.go`: Adopting detached agent runs (`AdoptLiveRun`) and CI jobs (`AdoptCIJob`), finishing runs across daemon restarts, and live-run registration for `chat` and `recreate_sandbox` by task ID (`RecreateByTask`).
- **Gaps closed in this audit:**
  1. **`SandboxRecreations.RecreateBySandbox(ctx, runID)` on adopted runs:** Extended `TestResumeAdoptedRunCanRecreateItsSandbox` in `pkg/orchestrator/adopt_test.go` to exercise `RecreateBySandbox` both while the adopted run is live and after completion (where it must return `ErrNoLiveSandboxForRun`). Previously `RecreateBySandbox` had zero callers in tests.
  2. **`pkg/daemon/runhostupgrade.go` lifecycle (`Flush`, `Resume`, `AdoptAll`):** Added `pkg/daemon/runhostupgrade_test.go` (`TestRunHostUpgradeNilAndFlushResumeLifecycle` and `TestAdoptAllWithHostdClaimsLiveSandboxAndMarksMissingSandboxLost`), verifying nil-receiver safety, `Flush`/`Resume` across daemon upgrades, live sandbox adoption via `hostd` socket re-attachment, and marking detached runs as lost when their sandbox no longer exists on the host.

### 4.4 GitHub Actions `workflow_run` / `workflow_job` / `check_run` Ingestion & `ci_mode: github` Gating

- **Existing coverage verified:**
  - `pkg/ghwebhook/cijob_test.go`: Parsing `workflow_job`, `check_run`, and `workflow_run` webhook payloads.
  - `pkg/orchestrator/githubci_test.go`: End-to-end GitHub CI run synthesis, job log-tail fetching, and check-run folding.
  - `pkg/forge/observe_test.go` & `pkg/forge/merge_rules_test.go`: `ci_mode: github` observation and `.grain/merge-rules.yml` path matching.
- **Gaps closed in this audit:**
  1. **`timed_out` step conclusions & `status` / `push` webhook parsing:** Added `TestParseWorkflowRunTimedOutStepAndStatusPushDeliveries` in `pkg/ghwebhook/cijob_test.go`, covering `timed_out` step conclusion mapping (`stepConclusion`), `ParseWorkflowRunEvent` metadata extraction, `ParseStatusEvent`, and `ParsePushEvent` (including branch deletion skips).
  2. **Synthesized 0-job `workflow_run` fallback:** Covered in `TestGitHubCIThreeSegmentRepoEventsTailsAndSynthesizedWorkflowRun` in `pkg/orchestrator/githubci_test.go`, verifying that a failed `workflow_run` with zero `workflow_job` records (e.g. a workflow file syntax/startup failure) synthesizes a `GitHubCIJob` with `HTMLURL` fallback so the failure is surfaced in `ci://` and PR status.
  3. **`ci_mode: github` interaction with `.grain/merge-rules.yml` (`grain`, `github`, `none`):** Added `TestCIModeGitHubGatingAndMergeRulesOverridesToGrainOrNone` in `pkg/forge/merge_rules_test.go`, verifying that a repository configured with `CIMode: "github"` gates merges on GitHub check runs by default, switches to Grain CI verdict gating when `.grain/merge-rules.yml` resolves `mode: grain` for the touched paths, and bypasses both GitHub check runs and Grain CI when `.grain/merge-rules.yml` resolves `mode: none` (e.g. docs-only changes).

### 4.5 Multi-Segment Forge Repositories (3-Segment `depot.code.forge.test/experimental/grain`)

- **Existing coverage verified:**
  - `pkg/model/repo_test.go`: `ParseRepoName` and `RepoName` methods for 2-segment and 3-segment repository names.
  - `pkg/gitproxy/commandforge_depot_test.go` & `pkg/gitproxy/host_routing_test.go`: Git proxy routing for multi-segment upstream repositories.
- **Gaps closed in this audit:**
  - Added `TestGitHubCIThreeSegmentRepoEventsTailsAndSynthesizedWorkflowRun` in `pkg/orchestrator/githubci_test.go`, exercising `NoteGitHubCIJob`, `NoteGitHubCIRun`, `splitStoredRepo` (which splits `"depot.code.forge.test/experimental/grain"` at the first `/` into owner `"depot.code.forge.test"` and repo `"experimental/grain"`), and failed-job log-tail fetching (`FetchJobLogTail`) for a 3-segment repository.
