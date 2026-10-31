# Test Suite Audit & Modernization (`tests.yml` & `nightly.yml`)

## 1. Executive Summary & Architectural Alignment

Over the past week, Grain's core architecture evolved significantly across seven subsystems:

1. **Repository Split & Retirement of `bwsalmon/grain`:** Core open-source development moved to `ai-on-gke/grain` alongside the 3-segment Google internal overlay `depot.code.corp.goog/experimental/grain` (`model.GrainSourceRepo()`).
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

Every pull request and `main` push runs the per-PR suite. Invariants in `tests/deploy/grain_workflows_test.go` (`TestTheGoJobsCoverEveryPackageInTheModuleOnce`, `TestTheGrainGoTestStepsStreamProgressThroughRunGoTests`) enforce that the five Go jobs partition `go list ./...` without gaps or overlaps and run with `-race`.

| Job | `.grain/workflows/tests.yml` | `.github/workflows/tests.yml` | Packages / Scope | Timeout |
| --- | --- | --- | --- | --- |
| `go` | Yes | Yes | `go vet ./...` + `go test -race ./cmd/... ./pkg/cli/...` (`cmd/grain`, `cmd/grain-agent`, `cmd/grain-guest-init`, `cmd/grain-hostd`, `cmd/grain-sandbox`, `pkg/cli`) | 20m |
| `go-e2e` | Yes | Yes | `go test -race ./tests/e2e/... ./tests/installer/bundle` | 20m |
| `go-orchestrator` | Yes | Yes | `go test -race -timeout 18m ./pkg/orchestrator/...` | 20m |
| `go-model` | Yes | Yes | `go test -race ./pkg/model/... ./pkg/staterepo/... ./pkg/ui/... ./tests/deploy/... ./tests/security/...` | 20m |
| `go-unit` | Yes | Yes | `go test -race` across all remaining `./pkg/...` packages (`agent`, `Alert`, `auth`, `backup`, `ci`, `claude`, `config`, `container`, `cost`, `crypto`, `daemon`, `db`, `forge`, `Fulltext`, `ghapp`, `ghwebhook`, `git`, `github`, `host`, `hostscript`, `k8s`, `logging`, `mcp`, `metrics`, `migrate`, `notify`, `omerge`, `proxy`, `ratelimit`, `redact`, `sandbox`, `sandboxcli`, `secrets`, `skills`, `ssh`, `store`, `supervisor`, `telemetry`, `tunnel`, `vcs`, `webhook`, `worktree`) | 20m |
| `ui` | Yes | Yes | `cd web && npm test && npm run build` (Vitest unit tests + Vite bundle) + `go test -race ./tests/installer/dryrun/...` | 15m |
| `ui-e2e` | Yes | Yes | `cd web && npx playwright test` (Playwright Chromium E2E against live Go backend) | 25m |
| `ui-webkit` | Yes | Yes | `cd web && npx playwright test --project=webkit` (Playwright WebKit / Mobile Safari layout & interaction E2E) | 25m |
| `helm-lint` | Yes | Yes | `helm lint charts/grain` + `helm template` rendering checks | 10m |
| `container` | — | Yes (GHA only) | Docker multi-stage image build (`Dockerfile`) + `grain --help` smoke check | 20m |
| `sandbox-smoke` | — | Yes (GHA only) | Container sandbox rootfs smoke test (`./scripts/test-sandbox.sh`) | 15m |

### 2.2 Nightly Suite (`.grain/workflows/nightly.yml` & `.github/workflows/nightly.yml`)

The nightly suite runs at `15 3 * * *` UTC (and via `workflow_dispatch`). `tests/deploy/grain_workflows_test.go` (`TestEverythingTheGateDoesNotRunTheNightlyDoes`, `TestTheNightlySuiteRunsTheLongTargets`, `TestThisRepositoryScheduledWorkflowsFitInsideTheirOwnCadence`) enforces that every build-tagged or slow target excluded from `tests.yml` is exercised in `nightly.yml` within the schedule cadence.

| Job | `.grain/workflows/nightly.yml` | `.github/workflows/nightly.yml` | Target / Scope | Timeout |
| --- | --- | --- | --- | --- |
| `go-long` | Yes | Yes (4 matrix shards) | `make test-long` (`go test -race -shuffle=on -count=2 ./...`) catching order-dependent and flaky race conditions | 45m (60m GHA) |
| `staterepo-soak` | Yes | Yes | `make test-staterepo-soak` (`go test -tags=soak -timeout 35m ./pkg/staterepo/...`) | 40m |
| `loadtest` | Yes | Yes | `make loadtest` (`go test -tags=loadtest -timeout 20m ./tests/e2e/... -run TestLoad`) | 25m |
| `scalability` | Yes | Yes | `make test-scalability` (`go test -tags=scalability -timeout 25m ./tests/e2e/... -run TestScalability`) | 30m |
| `merge-stress` | Yes | Yes | `make test-merge-stress` (`go test -tags=mergestress -timeout 25m ./tests/e2e/... -run TestMergeStress`) | 30m |
| `long-term-test` | Yes | Yes | `make long-term-test` (`go test -tags=longterm -timeout 55m ./tests/e2e/... -run TestLongTerm`) | 60m |
| `long-sim` | Yes | Yes | `make test-long-sim` (`go test -tags=longsim -timeout 40m ./tests/e2e/... ./pkg/model/... -run TestLongSim`) | 45m |
| `postgres-stress` | Yes | Yes | `make test-postgres` (real PostgreSQL service container + `./pkg/store/... ./pkg/migrate/... ./tests/e2e/...`) | 30m |
| `tunnel-stress` | Yes | Yes | `make test-tunnel-stress` (`go test -tags=tunnelstress -timeout 20m ./pkg/tunnel/...`) | 25m |
| `ui-e2e-long` | Yes | Yes | 3 consecutive Playwright passes (`for pass in 1 2 3; do npx playwright test; done`) | 45m |
| `installer-variants` | Yes | Yes | `go test -tags=installer_variants ./tests/installer/trixie/... ./tests/installer/hostmode/...` | 30m |
| `vulns` | Yes | Yes | `make vulns` (`govulncheck ./...` + `npm audit --omit=dev`) | 15m |
| `cmd-grain-stress` | Yes | — (Grain CI) | `go test -race -count=5 ./cmd/grain/... ./pkg/cli/...` | 20m |
| `kvm-sandboxes` | Yes | — (bare-metal KVM) | `go test -tags=kvm -timeout 25m ./pkg/sandbox/...` | 30m |
| `kvm-route-stress` | Yes | — (bare-metal KVM) | `go test -tags=kvm,routestress -timeout 25m ./pkg/sandbox/...` | 30m |
| `upgrade-test` | Yes | — (Grain CI) | `go test -tags=upgrade -timeout 25m ./tests/deploy/... -run TestUpgrade` | 30m |

### 2.3 Division of Responsibilities (`tests.yml` vs `nightly.yml`)

- **`tests.yml` (Per-PR Gate):** Fast (<20–25m wall clock), deterministic, single-pass (`-race`) functional and security verification across every Go package, Vitest unit suite, Playwright Chromium/WebKit E2E suites, installer bundle/dryrun checks, and Helm chart linting. No external network calls, multi-pass loops, or unasserted benchmark/measurement loops belong in `tests.yml`.
- **`nightly.yml` (Scheduled Deep Verification):** Multi-pass order/race fuzzing (`go-long` with `-race -shuffle=on -count=2` and `ui-e2e-long` 3x), build-tagged stress/soak/simulation suites (`soak`, `loadtest`, `scalability`, `mergestress`, `longterm`, `longsim`, `tunnelstress`), external database/distro matrices (`postgres-stress`, `installer-variants`), network-dependent vulnerability scanning (`vulns`), and bare-metal KVM/upgrade suites (`kvm-sandboxes`, `kvm-route-stress`, `upgrade-test`).

---

## 3. Pruned, Consolidated, and Modernized Tests

### 3.1 Removed Obsolete State-Repo Churn & Zero-Assertion Cost Measurement Files

1. **`pkg/staterepo/growth_test.go` (Deleted):**
   - `TestStateRepoGrowth` was written prior to `grain/task-395` to measure git object growth when `task_run` and `task_observation` rows were exported to the state repository. Following `grain/task-395`, `staterepo.ExportConfig` exports only configuration and memory tables, making the 6-pass `task_run`/`task_observation` loop a no-op against `grain/config` while still paying git commit/gc overhead on every PR.
   - `pkg/staterepo/staterepo_test.go` (`TestExportDoesNotWriteTaskOrCITables`) and `pkg/staterepo/soak_test.go` already directly verify `ExportConfig` invariants.
2. **`pkg/staterepo/cigrowth_test.go` (Deleted):**
   - `TestCIRunStorageCost` simulated 3 CI runs × 4 jobs × 6 steps (72 step logs) and ran 6 full `staterepo.Export` + `git count-objects` passes on every PR while explicitly stating `// and it asserts nothing`.
   - `TestCIStateRepoGrowth` asserted the absence of `tables/ci_step.json` after 3 multi-job CI runs, duplicating `TestExportDoesNotWriteTaskOrCITables` in `pkg/staterepo/staterepo_test.go`.
3. **`pkg/ci/runstore_live_cost_test.go` (Deleted):**
   - `TestLiveOutputWriteCost` executed 288 SQLite read/write operations across simulated 64 KiB step logs on every PR while explicitly stating `// It asserts nothing`.
   - The incremental step-log write invariant (`SaveLiveOutput` writing only the changed step's blob without rewriting the whole run) is already asserted by `TestALiveStepsOutputIsWrittenWithoutRewritingTheWholeRun` and `TestAPublicationThatMovesWhatOtherStepsKeepIsWrittenWhole` in `pkg/ci/runstore_stored_test.go`.

### 3.2 Consolidated Duplicate `security-audit` Job out of `nightly.yml`

- Removed the standalone `security-audit` job (`make security`, which runs `go vet ./...` + `go test -race -count=1 ./tests/security/...`) from `.grain/workflows/nightly.yml`, `.github/workflows/nightly.yml`, and the nightly table in `docs/github-actions-ci.md`.
- **Why:** As documented in `tests/security/doc.go`, `tests/security` is an ordinary package in the main module without build tags. Every PR already runs `go vet ./...` (in `go`) and `go test -race ./tests/security/...` (in `go-model`), and `nightly.yml`'s own `go-long` job (`make test-long`) already runs `./tests/security/...` twice with `-race -shuffle=on -count=2`.

### 3.3 Modernized Hardcoded `bwsalmon/grain` References

- Updated `tests/deploy/grain_workflows_test.go` (`TestTheGuestCacheJobIsWhatAPostMergeDispatchPlans` and `TestTheBaseBranchGateForThisRepositoryRunsItsTests`) to use `model.GrainSourceRepo().String()` (`"ai-on-gke/grain"`) instead of hardcoded `"bwsalmon/grain"`.
- Updated `pkg/sandboxcli/help.go` and `pkg/sandboxcli/sandboxcli_test.go` CLI examples from `bwsalmon/grain` to `ai-on-gke/grain`.

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

### 4.5 Multi-Segment Forge Repositories (3-Segment `depot.code.corp.goog/experimental/grain`)

- **Existing coverage verified:**
  - `pkg/model/repo_test.go`: `ParseRepoName` and `RepoName` methods for 2-segment and 3-segment repository names.
  - `pkg/sandbox/gitproxy_test.go` & `pkg/daemon/gitproxy_url_test.go`: Git proxy routing for multi-segment upstream repositories.
- **Gaps closed in this audit:**
  - Added `TestGitHubCIThreeSegmentRepoEventsTailsAndSynthesizedWorkflowRun` in `pkg/orchestrator/githubci_test.go`, exercising `NoteGitHubCIJob`, `NoteGitHubCIRun`, `splitStoredRepo` (which splits `"depot.code.corp.goog/experimental/grain"` at the first `/` into owner `"depot.code.corp.goog"` and repo `"experimental/grain"`), and failed-job log-tail fetching (`FetchJobLogTail`) for a 3-segment repository.
