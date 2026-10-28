# Nightly Prompt: Regression Triage & Test Suite Summary Orchestrator

## Objective
Act as the master orchestrator prompt for nightly test runs across the `gke-labs/granule` repository. Execute or aggregate results from all individual nightly test prompts (Quality Gates, Granulefile Conformance, Guest Lifecycle, VM Runtime & Networking, K8s & Docker Orchestration, Storage Benchmarks, Docs Audit, and Daily New Features Discovery), categorize any failures, isolate regressions from environmental flakes, draft root-cause analyses, file clear bugs as auto-merge follow-up tasks, and report only what the user needs to address.

## Context & Responsibilities
When scheduled nightly runs execute, the agent acting under this prompt is responsible for:
1. **Executing the Nightly Test Suite**: Running all test prompts defined in `.grain/prompts/` and `.grain/suites/nightly.yaml`.
2. **Aggregating Verdicts**: Collecting stdout/stderr logs, execution timings, and error codes from all test stages.
3. **Failure Classification**:
   - **Code Regression**: Introduced by recent commits on the branch or `main` (requires code fix or PR).
   - **External/Infrastructure Flake**: Runner resource exhaustion, network timeout to external registries (GHCR / Docker Hub), or missing kernel modules on host (requires retry or environment fix).
   - **Test Flake**: Non-deterministic race or timing issue in a test case.
4. **Acting on Findings**: Filing clear bugs as `propose_task` follow-ups with repro steps, commit culprits and remediation patches, and reporting to the user only the issues that need an architectural change or user intervention (see Reporting and Follow-up).

## Execution Workflow

### 1. Orchestrated Execution
Execute all nightly prompt tasks in sequence:
```bash
# 1. Quality Gates & Test Suite
echo "=== Running 01-test-suite-and-quality-gates ==="
go vet ./...
unformatted="$(gofmt -l ./cmd ./pkg)"
[ -z "$unformatted" ] || { echo "gofmt deviations in $unformatted"; exit 1; }
go test -v ./...
sudo modprobe tun || true
sudo env "PATH=$PATH" GRANULE_NETNS_TESTS=required go test -v -count=1 ./pkg/netshim/...

# 2. Granulefile Conformance
echo "=== Running 02-granulefile-conformance ==="
go test -v ./pkg/granulefile/...

# 3. Guest Lifecycle & Rebase
echo "=== Running 03-guest-image-lifecycle ==="
go test -v ./pkg/guestimage/... ./pkg/guestpack/... ./pkg/guestrebase/... ./pkg/guestverify/...

# 4. VM Runtime & Networking
echo "=== Running 04-vm-runtime-and-networking ==="
go test -v ./pkg/hypervisor/... ./pkg/agent/... ./pkg/dockervm/...

# 5. K8s & Docker Orchestration Dry Run
echo "=== Running 05-k8s-and-docker-orchestration ==="
demo/demo.sh --dry-run --no-color
deploy/publish-images.sh --dry-run --no-login all

# 6. Benchmark Suite & Performance Verification
echo "=== Running 06-storage-and-benchmarks ==="
find benchmarks -name "*.sh" -exec bash -n {} +
./benchmarks/run_all_benchmarks.sh --dry-run

# 7. Documentation & Spec Audit
echo "=== Running 07-docs-and-spec-audit ==="
find deploy benchmarks demo -name "*.sh" -exec bash -n {} +

# 8. Daily New Features Discovery & Test Plan Execution
echo "=== Running 09-daily-new-features-test-plan ==="
COMMITS=$(git log --since="24 hours ago" --no-merges --format="%H %s")
[ -n "$COMMITS" ] || COMMITS=$(git log -n 5 --no-merges --format="%H %s")
echo "Analyzed daily commits: $COMMITS"
```

### 2. Failure Triage & Root Cause Analysis
If any step exits non-zero:
- Inspect `git log -n 5` to identify recent changes touching the affected component.
- Run `git diff` against previous known-good state if applicable.
- Isolate whether the failure reproduces with `-count=1` and determine if race conditions exist.

### 3. Reporting and Follow-up
Report only what the user needs to address. Every outcome falls into exactly one of three cases:
- **Everything is fine**: All checks pass, or the only failures are infrastructure or test flakes that cleared on a fresh `-count=1` rerun. File nothing and do not report.
- **Clear bugs**: A regression with an identified culprit and an obvious fix. File a follow-up task per bug via `propose_task` with `auto_merge: true`, and do not report it. Each task carries:
  - Component and file path.
  - Minimal reproducer script or test command.
  - Full error log and stack trace.
  - Proposed fix.
- **Otherwise**: An issue that a new task cannot fix, because it needs an architectural change or user intervention (a design decision, a credential, host or infrastructure access). Report it to the user with:
  - A minimal reproducer script or test command.
  - An analysis of the architectural impact: which components are affected and why a direct fix is not enough.
  - Specific decision points the user must resolve before work can proceed.

## Evaluation & Reporting Criteria
1. **When to Report**: Report to the user only in the third case above. A run that is all green, or whose findings are all filed as `auto_merge: true` follow-up tasks, ends without a report.
2. **Report Contents**: For each issue the user must address, give the minimal repro, the architectural impact analysis and the decision points. List any follow-up tasks filed in the same run by task ID only, without restating their contents.
3. **Closing Comment Format**: Use structured markdown suitable for grain task conversations and GitHub issues.
