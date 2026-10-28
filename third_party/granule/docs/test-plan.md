# Granule Comprehensive Test Plan & Verification Strategy

## 1. Overview & Testing Philosophy

Granule bridges container developer ergonomics with hardware-isolated Cloud-Hypervisor microVM execution. Because Granule interfaces directly with Linux kernel features (`KVM`, `TUN/TAP`, `splice()`, `virtio-mem`, `virtio-block`, `vsock`, `cgroups`, network namespaces), comprehensive testing requires a layered verification strategy spanning:
- Fast, deterministic **offline unit and compiler tests** running in CI without special privileges.
- Privileged **kernel datapath and namespace tests** (`CAP_NET_ADMIN`, `/dev/net/tun`).
- Full **microVM boot and guest lifecycle tests** requiring hardware virtualization (`/dev/kvm`).
- **Orchestration and Multi-VM Pod integration tests** in Docker and Kubernetes/GKE environments.
- **Unified performance and I/O telemetry benchmarks** guarding boot latency (<100ms), syscall latency (<60ns), memory scaling (<500ms), and storage throughput.
- **Continuous Nightly Agent Verification** using autonomous Grain coding agents.

This document serves as the master test plan for the `gke-labs/granule` repository.

---

## 2. Test Architecture & Verification Matrix

```
+-------------------------------------------------------------------------+
|                  9. Nightly Agent Triage & Continuous Verification      |
|                 10. Daily New Features Test Plan & Auto-Walkthroughs    |
+-------------------------------------------------------------------------+
|  5. Kubernetes Multi-VM Pods  |  7. Unified Performance & Telemetry     |
|     & Docker Orchestration    |     (Boot Latency, Syscalls, I/O)       |
+-------------------------------+-----------------------------------------+
|  3. Guest Image Lifecycle     |  4. MicroVM Runtime, Spliced Network   |
|     & Rebase Invariants       |     & Vsock/Memory Control Plane        |
+-------------------------------+-----------------------------------------+
|  2. Granulefile Conformance   |  6. Storage Architecture & VMDK         |
|     & Multi-Stage Compiler    |     Multi-Extent Reflink Overlays       |
+-------------------------------+-----------------------------------------+
|  1. Quality Gates, Unit Tests, Race Detection & Kernel Network Tests    |
|  8. Documentation, CLI Flags & Specification Consistency Audit          |
+-------------------------------------------------------------------------+
```

### Verification Matrix by Tier

| Test Subsystem | Scope / Target | Execution Mode | Environment Reqs | Target Frequency |
| :--- | :--- | :--- | :--- | :--- |
| **Sub-Part 1: Quality Gates** | `cmd/*`, `pkg/*` unit tests, race detector, static analysis, netns tests | Automated | Linux, Go 1.22+, `tun`, root for netns | Every PR / CI & Pre-commit |
| **Sub-Part 2: Compiler Conformance** | Granulefile parser, AST, compiler, §14 worked examples | Automated | Offline (no Docker/KVM needed) | Every PR / CI |
| **Sub-Part 3: Guest Image Lifecycle** | Guest rootfs overlays (Debian/Alpine), boot contracts, `guest pack`, `guest rebase` | Automated + By-Hand | Docker, Registry / GHCR | Nightly & Image Releases |
| **Sub-Part 4: VM Runtime & Datapath** | `granule vm` CLI, Cloud-Hypervisor, vsock `granule-agent`, `splice()` tap/veth | Automated + By-Hand | `/dev/kvm`, `/dev/net/tun`, Docker | PR Integration & Nightly |
| **Sub-Part 5: K8s & Orchestration** | Pod specs, Multi-VM Pods, ephemeral debug microVMs, demo rehearsal | Automated + Manual | GKE / K8s Cluster, nested KVM | Nightly & Pre-release |
| **Sub-Part 6: High-Throughput Storage** | VMDK 64MB extent overlays, reflink COW, persistent disk mode, snapshot/restore | Automated | Host filesystem with reflink | PR & Nightly |
| **Sub-Part 7: Performance Benchmarks** | MicroVM boot (<100ms), syscalls (<60ns), `virtio-mem` (<500ms), network/disk I/O | Automated | Bare metal / Nested KVM GCE | Nightly & Release Gating |
| **Sub-Part 8: Docs & Spec Audit** | CLI flags (`granule --help`), zero stale legacy names, spec alignment | Automated | Standard runner | Every PR / CI & Nightly |
| **Sub-Part 9: Nightly Regression Triage** | End-to-end execution of all nightly prompts, regression categorization | Agent Orchestrated | Grain Sandbox with KVM/Docker | Nightly (02:00 UTC) |
| **Sub-Part 10: Daily New Features Test Plan** | 24-hour change discovery, automated walkthrough design, auto-merge task dispatch | Agent Orchestrated | Grain Sandbox with `propose_task` | Nightly (02:00 UTC) |

---

## 3. Detailed Sub-Part Plans

### Sub-Part 1: Core Quality Gates, Unit Tests, Race Detection & Kernel Networking

#### Objective
Ensure zero regressions in foundational Go logic, data race safety, static analysis compliance, and kernel networking tap/veth interface management.

#### Automated Test Suite
- **Code Formatting & Linting**:
  ```bash
  gofmt -l ./cmd ./pkg
  go vet ./...
  ```
- **Package Unit Tests with Race Detection**:
  ```bash
  go test -race ./...
  ```
- **Privileged Kernel Network Namespace Tests**:
  `pkg/netshim` creates real network namespaces (`CLONE_NEWNET`), TAP devices, and `tc` filters.
  ```bash
  sudo modprobe tun || true
  sudo env "PATH=$PATH" GRANULE_NETNS_TESTS=required go test -v -count=1 ./pkg/netshim/...
  ```
- **Shell Script Validation**:
  ```bash
  find deploy benchmarks demo -name "*.sh" -exec bash -n {} +
  ```

#### Invariants & Acceptance Criteria
- All Go packages report `PASS` without data race warnings.
- `GRANULE_NETNS_TESTS=required` passes without skipping TAP/tc setup.
- Shell scripts pass `bash -n` syntax validation.

---

### Sub-Part 2: Granulefile Specification Conformance & Multi-Stage Compiler

#### Objective
Validate the Granulefile compiler (`pkg/granulefile`, `granule granulefile check`, `granule granulefile build`) against the complete language specification (`docs/granulefile.md`).

#### Automated Test Suite
- **Compiler Unit Tests & Specification Examples**:
  ```bash
  go test -v ./pkg/granulefile/... ./pkg/guestdockerfile/...
  ```
  Exercises §14.1 through §14.9 worked examples in `test/granulefiles/` offline without Docker or KVM.
- **AST Parser, Lexer & Negative Rejection Tests**:
  Verifies proper syntax error reporting for invalid stage transitions, missing kernels, circular dependencies, and illegal directive placements.
- **Granulefile CLI Validation**:
  ```bash
  granule granulefile check -f test/granulefiles/01-dockerfile-is-a-granule/Granule
  granule granulefile check -f test/granulefiles/02-wrap-in-a-machine/Granule
  ```

#### Invariants & Acceptance Criteria
- Full coverage of `VM FROM`, `VM PACK`, `VM PACK FROM`, `VM WRAP`, `VM WRAP FROM`, `VM UNPACK FROM`, `VOLUME`, `ENTRYPOINT`, and `CMD`.
- Multi-stage intermediate Dockerfile synthesis matches expected stage DAG and metadata.
- Level 3 unsupported constructs (`RUN` in booted VM stage) fail with explicit descriptive errors.

---

### Sub-Part 3: Guest Image Lifecycle, Rootfs Packaging & Rebase Safety

#### Objective
Validate guest OS overlays (Debian Bookworm, Alpine Linux), boot contract integrity (`dev.granule.guest.contract`), guest packing (`granule guest pack`), and three-tier rebase mechanics (`granule guest rebase`).

#### Automated Test Suite
- **Guest Subsystem Unit Tests**:
  ```bash
  go test -v ./pkg/guestimage/... ./pkg/guestpack/... ./pkg/guestrebase/... ./pkg/guestverify/... ./pkg/guestboot/... ./pkg/guestcp/...
  ```
- **Image Publishing Dry Run**:
  ```bash
  deploy/publish-images.sh --dry-run --no-login all
  ```
  Verifies that tag schemas (`:latest`, `:alpine`, `:debian12`, `:github-ubuntu-slim`) and guest rootfs pairings are accurately constructed.
- **Guest Overlay Asset Verification**:
  ```bash
  find deploy/guest-image -type f -name "*.sh" -exec bash -n {} +
  test -f deploy/guest-image/overlay-common/etc/granule-guest-version
  test -f deploy/guest-image/overlay-debian/etc/acpi/powerbtn.sh
  test -f deploy/guest-image/overlay-alpine/etc/acpi/powerbtn.sh
  ```

#### Manual / Host Boot Verification (with KVM)
- Build guest rootfs and pack bootable microVM image:
  ```bash
  docker build -t granule:latest .
  docker build --target guest-rootfs-image -t granule-guest-rootfs:local .
  granule guest pack -from granule-guest-rootfs:local -t granule-test-vm:latest -kontur-image granule:latest
  granule guest verify -image granule-test-vm:latest -kontur-image granule:latest -v
  ```
- Test all three rebase modes:
  1. Cheap OCI metadata/VMM rebase (`granule guest rebase -from <img-old> -t <img-new> -kontur-image granule:latest`).
  2. Full rootfs repack rebase (`granule guest rebase -from <img-old> -rootfs <rootfs-img> -t <img-new>`).
  3. Live in-guest boot injection rebase (`granule guest rebase -from <img-old> -boot -t <img-new>`).

#### Invariants & Acceptance Criteria
- Contract constant (`guestimage.Contract`), rootfs overlay file (`etc/granule/guest-contract`), and Dockerfile label match across all builds (`TestContractIsDeclaredEverywhere`).
- Rebase refuses images without kernels or incompatible contract floors.

---

### Sub-Part 4: MicroVM Runtime, Spliced Datapath & Control Plane

#### Objective
Exercise Cloud-Hypervisor microVM process management, vsock control stream (`cmd/granule-agent`, `pkg/agent`), streaming terminal execution (`pkg/execwire`), memory agent elasticity (`cmd/granule-mem-agent`, `pkg/memagent`), and the VM CLI (`pkg/cli`, `pkg/dockervm`).

#### Automated Test Suite
- **Runtime and Hypervisor Unit Tests**:
  ```bash
  go test -v ./pkg/hypervisor/... ./pkg/agent/... ./pkg/dockervm/... ./pkg/cli/... ./pkg/memagent/... ./pkg/execwire/...
  ```

#### Manual / Host Verification (with KVM & Docker)
- **Lifecycle Command Verification**:
  ```bash
  granule vm create test-vm -cpus 2 -memory-mb 1024 -state-dir /tmp/granule-test-state --wait
  granule vm status test-vm -state-dir /tmp/granule-test-state
  granule vm exec test-vm -state-dir /tmp/granule-test-state -- uname -a
  granule vm delete test-vm -state-dir /tmp/granule-test-state
  ```
- **Multi-VM Concurrency & Stress Test**:
  ```bash
  NUM_VMS=3 ./test/test-multi-vm.sh
  ```
  Validates concurrent VM execution, state-dir isolation, CPU/Disk I/O stress, and inter-VM networking.

#### Invariants & Acceptance Criteria
- MicroVM cleanly transitions through `Creating` -> `Booting` -> `Ready` -> `Stopped` -> `Deleted`.
- In-guest exit codes, stdout, stderr, and TTY window resizes faithfully propagate over vsock.
- Spliced networking achieves TAP-to-veth forwarding without dropping packets.

---

### Sub-Part 5: Kubernetes Multi-VM Pods & Container Orchestration

#### Objective
Validate Kubernetes Pod manifests, Multi-VM Pod execution (1:1 container-to-microVM mapping in shared Pod namespaces), ephemeral debug containers (`kubectl debug`), and standalone Docker workflows.

#### Automated Test Suite
- **Manifest Syntax & Schema Validation**:
  ```bash
  python3 -c '
  import yaml, glob
  for path in sorted(glob.glob("deploy/k8s/*.yaml") + glob.glob("demo/assets/*.yaml")):
      with open(path) as f:
          docs = list(yaml.safe_load_all(f))
          print(f"Validated {path} ({len(docs)} docs)")
  '
  ```
- **Docker CLI Unit Tests**:
  ```bash
  go test -v ./pkg/dockervm/... ./pkg/dockercli/...
  ```
- **Presenter Demo Dry-Run**:
  ```bash
  demo/demo.sh --dry-run --no-color
  ```
  Ensures all 7 chapters (`provision`, `publish`, `local`, `gke`, `pod`, `build`, `rebase`) pass without errors.

#### Invariants & Acceptance Criteria
- Kubernetes manifests specify appropriate `/dev/kvm` hostPath mounts and security contexts.
- Multi-VM Pods maintain hardware VM isolation while sharing Pod networking.
- `demo.sh --dry-run` confirms all chapters and steps are functional.

---

### Sub-Part 6: High-Throughput Storage & QCOW2 Overlays

#### Objective
Validate QCOW2 overlays (2MB cluster size, reflink COW cloning on flat boot), raw persistent disk mode (`CHV_DISK_MODE=persistent`), small layer extensions, flatten replay, and snapshot/restore state persistence.

#### Automated Test Suite
- **QCOW2 Subsystem Unit Tests**:
  ```bash
  go test -v ./pkg/qcow2/...
  ```
  Verifies header parsing, 2MB cluster allocation, reflink clone probing, virtual disk expansion, small layer extension, flatten replay, and state preservation during snapshot/restore.

#### Invariants & Acceptance Criteria
- Reflink extent cloning performs instantaneous copy-on-write without physical data duplication on supported filesystems (Btrfs, XFS, tmpfs).
- Persistent disk mode delivers 4x-5.7x higher write IOPS over legacy QCow2 overlays.
- Snapshot restore preserves all modified extent blocks.

---

### Sub-Part 7: Unified Performance Benchmarking & Telemetry

#### Objective
Continuously verify that Granule meets its performance targets: sub-100ms microVM boot latency, <60ns direct syscall latency, <500ms `virtio-mem` elasticity, and high-throughput spliced networking.

#### Automated Test Suite
- **Dry-Run Benchmark Runner**:
  ```bash
  ./benchmarks/run_all_benchmarks.sh --dry-run
  ```
- **Full Benchmark Suite Execution (on KVM hardware)**:
  ```bash
  ./benchmarks/run_all_benchmarks.sh --suite=all --out-dir=benchmarks/results
  ```

#### Performance Thresholds & Invariants
- **Boot Latency**: Direct kernel boot initialization < 100ms.
- **Syscall Overhead**: In-guest `getpid` / `gettimeofday` latency < 60ns (compared to >1,800ns for ptrace/gVisor).
- **Memory Elasticity**: Stepped expansion (1GB -> 4GB) < 500ms total latency; idle host RSS < 150MB.
- **Storage Direct I/O**: High IOPS on 4K random read/write without hypervisor lockups.
- **Network Throughput**: > 90% bare-metal Docker bridge throughput.

---

### Sub-Part 8: Documentation, API & Specification Consistency Audit

#### Objective
Audit documentation against actual Go CLI subcommands, flags, and types to eliminate documentation drift and remove obsolete project references.

#### Automated Test Suite
- **CLI Flag & Subcommand Audit**:
  ```bash
  go build -o /tmp/granule ./cmd/granule
  for cmd in "vm create" "vm run" "vm exec" "vm shell" "vm wait" "vm status" "vm update" "vm delete" "vm list" \
             "guest build" "guest pack" "guest rebase" "guest verify" \
             "granulefile check" "granulefile build"; do
    /tmp/granule --help | grep -Fq "$cmd" || { echo "Missing $cmd"; exit 1; }
  done
  ```
- **Legacy Name Audit**:
  Ensure no active code or documentation uses legacy names without qualification:
  ```bash
  grep -rnEi "\bkonturctl\b|\bKonturfile\b" docs/ pkg/ cmd/ || true
  ```

---

### Sub-Part 9: Nightly Continuous Verification & Regression Triage

#### Objective
Execute the full suite of nightly agent prompts defined in `.grain/prompts/` on a daily schedule (02:00 UTC), aggregate results, classify regressions vs flakes, and generate triage reports.

#### Nightly Suite Configuration
Configured in `.grain/suites/nightly.yaml`, `.grain/templates/nightly.yaml`, and `.grain/schedules/nightly.yaml`:
1. `01-test-suite-and-quality-gates.md`
2. `02-granulefile-conformance-and-compiler.md`
3. `03-guest-image-lifecycle-and-rebase.md`
4. `04-vm-runtime-networking-and-control-plane.md`
5. `05-k8s-and-docker-orchestration.md`
6. `06-storage-and-performance-benchmarks.md`
7. `07-docs-and-api-spec-consistency.md`
8. `09-daily-new-features-test-plan.md`
9. `08-nightly-regression-triage-and-summary.md` (Master triage orchestrator)

#### Automated Orchestrator & Triage Runner
Nightly runs and local triage verification can be executed end-to-end using the automated suite runner:
```bash
# Execute full nightly verification suite and produce triage reports:
test/run-nightly-suite.sh --report=/tmp/nightly-report.md --json-report=/tmp/nightly-report.json

# Fast dry-run mode:
test/run-nightly-suite.sh --dry-run
```

---

### Sub-Part 10: Daily New Features Test Plan & Autonomous Walkthroughs

#### Objective
Analyze all features, subcommands, flags, compiler directives, and runtime options merged into `gke-labs/granule` in the previous 24 hours. Synthesize targeted test plans, edge-case validations, and end-to-end user walkthroughs, then dispatch autonomous agent tasks with `auto_merge: true` via `propose_task` to execute walkthroughs, isolate defects, author in-tree fixes, and auto-merge patches.

#### Automated Feature Discovery & Task Dispatch
- **Daily Git Analysis**:
  ```bash
  git log --since="24 hours ago" --no-merges --stat
  ```
- **Walkthrough Generation**: Formulates positive workflows, edge cases (concurrency, resource limits), and negative/error diagnostics.
- **Autonomous Task Proposing**: Calls `propose_task` with `auto_merge: true`, detailed reproduction steps, and in-tree fix instructions.

---

## 4. Defect Triage & Remediation Workflow

When a test failure occurs during CI, local runs, or nightly verification:
1. **Reproduce & Classify**:
   - **Code Regression**: Deterministic failure in `pkg/` or `cmd/` logic -> Author code patch with regression unit test.
   - **Infrastructure / Environment Outage**: Missing host `/dev/kvm`, `/dev/net/tun`, or external registry rate limit -> Mark as infra flake, retry or configure environment.
   - **Flaky Concurrency / Timing**: Non-deterministic race in vsock or goroutine teardown -> Isolate with `-race` and `-count=100`, fix synchronization primitive.
2. **File or Fix**:
   - Fix in-tree immediately if within current task scope.
   - File structured task with reproduction steps via `propose_task` if out-of-scope or requiring separate review.
3. **Verify Remediation**:
   - Re-run package test with `-race -count=5`.
   - Run full CI checks via `wait_for_checks`.
