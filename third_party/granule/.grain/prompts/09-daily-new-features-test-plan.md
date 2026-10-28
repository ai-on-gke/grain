# Nightly Agent Test Prompt: Daily New Features Discovery, Test Design & Walkthrough Execution

## Objective
Automatically discover and analyze all new features, CLI subcommands, Granulefile directives, hypervisor/kernel runtime capabilities, storage architectures, and API enhancements merged or submitted to `gke-labs/granule` in the previous 24 hours (or since the last nightly cycle).

For every newly introduced feature:
1. Design comprehensive end-to-end user walkthroughs, integration test plans, and edge-case/negative verification scenarios.
2. File autonomous follow-up agent tasks with `auto_merge: true` via `propose_task` to execute these walkthroughs, isolate and debug any runtime issues or regressions, author in-tree code fixes and regression unit tests, verify that all CI checks pass, and automatically submit/merge the fixes.

## Context & Scope
- **Target Repository**: `gke-labs/granule`
- **Execution Model**: Dispatched daily at 02:00 UTC as part of the native `nightly-test-suite` declared in [`.grain/suites/nightly.yaml`](../suites/nightly.yaml).
- **Core Architecture Areas**:
  - **Granulefile Parser & Compiler** (`pkg/granulefile`, `pkg/guestdockerfile`, `docs/granulefile.md`): Directives (`VM FROM`, `VM PACK`, `VM WRAP`, `VM UNPACK FROM`, `VOLUME`, `KERNEL`), multi-stage intermediate representations, and stage translation.
  - **MicroVM Runtime & Control Plane** (`cmd/granule`, `pkg/cli`, `pkg/hypervisor`, `pkg/dockervm`, `pkg/agent`, `pkg/memagent`): Cloud-Hypervisor lifecycle (`create`, `run`, `exec`, `shell`, `wait`, `status`, `update`, `delete`, `list`), vsock multiplexing, and dynamic memory resizing.
  - **Kernel Datapath & Networking** (`pkg/netshim`): TAP interface creation, spliced in-kernel forwarding (`splice()`), network namespaces (`CLONE_NEWNET`), and `tc` traffic control.
  - **High-Throughput Storage & QCOW2** (`pkg/qcow2`): 2MB cluster QCOW2 overlays, reflink copy-on-write, flat persistent disks (`CHV_DISK_MODE=persistent`), small layer extensions, flatten replay, and snapshot/restore workflows.
  - **Guest OS Packaging & Rebase** (`pkg/guestimage`, `pkg/guestpack`, `pkg/guestrebase`, `pkg/guestverify`): Debian/Alpine overlays, ACPI power contracts, and runtime rebase integrity.
  - **Orchestration & Deployments** (`deploy/`, `test/multivm`, `demo/`): Kubernetes Multi-VM Pods (1:1 container-to-VM) and Docker orchestration.

---

## Execution Workflow

### 1. Discover Daily Feature Submissions & Changes
Inspect the git history across the repository for commits and feature changes merged in the last 24 hours:

```bash
# Query git commits merged in the last 24 hours (excluding merge commits)
DAILY_COMMITS=$(git log --since="24 hours ago" --no-merges --format="%H %s")

# Fallback to recent commits if no commits were merged in the last 24 hours
if [ -z "$DAILY_COMMITS" ]; then
  echo "No commits merged in the last 24 hours; inspecting recent commits on main"
  DAILY_COMMITS=$(git log -n 5 --no-merges --format="%H %s")
fi

echo "=== Commits to Analyze for New Features ==="
echo "$DAILY_COMMITS"
```

For each commit identified:
1. Review the detailed diff and modified file paths:
   ```bash
   git show --stat "$COMMIT_HASH"
   git diff "$COMMIT_HASH"^!
   ```
2. Classify the feature into one or more categories:
   - `NEW_CLI_COMMAND_OR_FLAG`: Added flags or subcommands in `cmd/granule` / `pkg/cli/`.
   - `NEW_GRANULEFILE_SYNTAX`: New directives, stage translation rules, or compiler checks in `pkg/granulefile/`.
   - `RUNTIME_OR_HYPERVISOR`: Cloud-Hypervisor options, memory agent changes, vsock protocol changes.
   - `KERNEL_NETWORKING`: Network namespace, TAP device, or splice forwarding changes in `pkg/netshim/`.
   - `STORAGE_QCOW2`: QCOW2 overlay layout, disk mode flags, snapshot or reflink handling in `pkg/qcow2/`.
   - `GUEST_IMAGE_LIFECYCLE`: Rootfs packaging, kernel injection, or rebase logic in `pkg/guest*/`.
   - `K8S_ORCHESTRATION`: Pod specs, multi-VM pod wrappers, or deployment scripts in `deploy/` or `test/`.

### 2. Design Walkthroughs, Test Scenarios & Edge Cases
For each discovered feature, synthesize a comprehensive test plan containing:

1. **Happy Path User Journey**:
   - Realistic end-to-end developer command sequence (e.g. authoring a Granulefile, running `granule vm run`, executing in-guest commands).
2. **Boundary Conditions & Stress Scenarios**:
   - Concurrency tests (e.g. multiple microVMs launched in parallel).
   - Resource limits (e.g. low memory, disk saturation, rapid start/stop cycles).
   - Disk mode combinations (e.g. testing both `persistent` and `overlay` modes).
3. **Negative Tests & Error Diagnostics**:
   - Invalid flag parameters, missing kernel images, corrupt VMDK descriptors.
   - Asserting that clear, user-friendly error messages are returned instead of panics or unhandled hypervisor faults.
4. **Backward Compatibility & Regression Invariants**:
   - Verifying that existing Granulefiles in `test/granulefiles/` continue to compile and validate without error.

### 3. File Autonomous Follow-up Agent Tasks via `propose_task`
For each newly submitted feature requiring deep verification and bug remediation, call the `propose_task` tool to file an autonomous agent task configured for auto-merge:

- **`title`**: `Nightly Walkthrough & Fix: <Feature Name> (<Component>)`
- **`auto_merge`**: `true` (enables the follow-up agent to automatically merge code fixes once tests pass)
- **`repo`**: `gke-labs/granule`
- **`depends_on`**: Array containing the current task ID (so walkthroughs execute with clean dependencies)
- **`body`**: Structured markdown containing:
  - **Feature Metadata**: Commit SHA, author, summary, and modified components.
  - **Walkthrough Steps**: Reproducible CLI commands, Granulefiles, or test scripts to execute.
  - **Verification Assertions**: Expected exit codes, log output substrings, and state directory contents.
  - **Bug Remediation Protocol**:
    1. If a test fails, isolate the bug and write a reproducible Go unit test in `pkg/` or integration test in `test/`.
    2. Implement the fix in the source code.
    3. Run full test suites (`go test -race ./...`, `sudo env "PATH=$PATH" GRANULE_NETNS_TESTS=required go test ./pkg/netshim/...`).
    4. Commit with a structured commit message explaining the defect and fix.
    5. Push to branch `grain/task-<id>` and verify CI pass via `wait_for_checks`.
    6. Allow the PR to auto-merge upon green CI verdict.

### 4. Immediate Verification & Sanity Check
Ensure all local tests pass and verify prompt integrity:

```bash
# Verify Go unit tests and prompt suite integrity
go test -v ./pkg/cli -run TestNightlyPromptsAndSuite

# Validate shell script syntax across repository
find deploy benchmarks demo test -name "*.sh" -exec bash -n {} +
```

---

## Evaluation & Reporting Criteria

1. **Daily Features Catalog**:
   - Table of analyzed commits from the past 24 hours with commit hash, author, feature classification, and affected files.
2. **Designed Walkthrough Specifications**:
   - Full test plans, executable CLI commands, and test Granulefile manifests designed for each new feature.
3. **Filed Autonomous Tasks Summary**:
   - The follow-up tasks proposed via `propose_task` (task IDs, titles, `auto_merge: true`, dependency linkages) are recorded by grain itself; do not restate them in a report.
4. **Closing Summary & Reporting Policy**: Report only what the user needs to address.
   - If there were no new features to cover, or every new feature is covered by a filed follow-up task, file nothing further and do not report.
   - For clear bugs found while designing the walkthroughs, file follow-up tasks via `propose_task` with `auto_merge: true` and do not report them.
   - Report to the user only an issue that a new task cannot fix, because it needs an architectural change or user intervention, with a minimal repro, an analysis of the architectural impact and specific decision points.
