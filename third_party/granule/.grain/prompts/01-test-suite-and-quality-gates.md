# Nightly Prompt: Test Suite & Quality Gate Verification

## Objective
Execute the full unit test suite, race detector, static analysis, kernel networking integration tests, and shell script syntax checks across the `gke-labs/granule` repository to verify overall code health, identify regressions, and catch race conditions or flaky tests.

## Context & Prerequisites
- **Target Repository**: `gke-labs/granule`
- **Environment**: Linux x86_64 / Debian runner with Go 1.22+, `tun` kernel module loaded, root privileges available for kernel network tests, and Docker daemon access if available.
- **Key Packages**:
  - Commands: `cmd/granule`, `cmd/granule-agent`, `cmd/granule-container`, `cmd/granule-mem-agent`, `cmd/granule-shim`
  - Core internals: `pkg/agent`, `pkg/cli`, `pkg/config`, `pkg/containerspec`, `pkg/dockercli`, `pkg/dockervm`, `pkg/execwire`, `pkg/granulefile`, `pkg/guestboot`, `pkg/guestbuild`, `pkg/guestcp`, `pkg/guestdockerfile`, `pkg/guestexec`, `pkg/guestimage`, `pkg/guestpack`, `pkg/guestrebase`, `pkg/guestverify`, `pkg/guestwrap`, `pkg/hypervisor`, `pkg/memagent`, `pkg/netshim`, `pkg/qcow2`, `pkg/vmspec`

## Execution Steps

### 1. Code Formatting and Static Analysis
Run formatting and vetting across all Go sources to ensure zero style and syntax regressions:
```bash
# Check gofmt formatting
unformatted="$(gofmt -l ./cmd ./pkg)"
if [ -n "$unformatted" ]; then
  echo "gofmt deviations found in:"
  echo "$unformatted"
  exit 1
fi

# Run go vet
go vet ./...
```

### 2. Standard Unit and Package Tests
Run all unit and package tests across all subdirectories with verbose output and package timings:
```bash
go test -v ./...
```
- Verify all packages report `ok`.
- Measure per-package execution times and flag any package taking abnormally long (> 30s).

### 3. Concurrency and Race Detection
Run the Go race detector to uncover any data races in concurrent paths (such as `pkg/agent`, `pkg/hypervisor`, `pkg/dockervm`, `pkg/netshim`):
```bash
go test -race ./cmd/... ./pkg/...
```

### 4. Kernel Networking Integration Tests (as Root)
The `pkg/netshim` package contains integration tests that create real network namespaces, TAP devices, and tc filters. Verify these execute properly:
```bash
sudo modprobe tun || true
sudo env "PATH=$PATH" GRANULE_NETNS_TESTS=required go test -v -count=1 ./pkg/netshim/...
```
- Verify that tests do not skip when `GRANULE_NETNS_TESTS=required` is set.
- Check TAP interface creation, splice datapath forwarding, and cleanup.

### 5. Shell Script Syntax and Rehearsals
Check syntax for all operational and benchmark shell scripts:
```bash
find deploy benchmarks demo -name "*.sh" -exec bash -n {} +

# Run presenter demo rehearsal dry-run
demo/demo.sh --dry-run --no-color

# Run image publish dry-run check
deploy/publish-images.sh --dry-run --no-login all
```

## Evaluation & Reporting Criteria
1. **Pass/Fail Status**: Detail the status of each check (gofmt, vet, unit tests, race detector, netns tests, shell syntax, dry runs).
2. **Performance Metrics**: Report package execution times, noting any bottlenecks or timing anomalies.
3. **Failure Diagnosis**: If any test fails, provide:
   - Failing test name and file location.
   - Exact stdout/stderr failure logs and stack trace.
   - Whether the failure appears to be a regression introduced by recent commits or an environmental defect.
4. **Actionable Recommendations & Reporting Policy**: Report only what the user needs to address.
   - If every check passes, file nothing and do not report: a green run stays silent.
   - For a clear bug with an obvious fix, file a follow-up task via `propose_task` with `auto_merge: true` carrying the failing test, logs and proposed patch, and do not report it.
   - Report to the user only an issue that a new task cannot fix, because it needs an architectural change or user intervention (a decision, a credential, host access).
