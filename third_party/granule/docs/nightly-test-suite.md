# Granule Nightly Agent Test Suite & Continuous Verification

This document outlines the architecture, execution model, and catalog of the nightly automated test suite for the Granule repository (`gke-labs/granule`).

---

## 1. Overview & Motivation

While continuous integration (CI) gates pull requests on unit tests and dry-run syntax checks, nightly test runs provide deeper continuous verification by dispatching AI coding agents with specialized testing prompts.

Nightly agent tasks evaluate:
- Deep compiler conformance against edge cases and worked examples.
- Kernel-level networking and datapath performance.
- Multi-VM Kubernetes Pod and Docker runtime behavior.
- Storage I/O throughput and microVM startup latency regressions.
- Documentation and specification drift.

The suite configuration is located at [`.grain/suites/nightly.yaml`](../.grain/suites/nightly.yaml), with template definition at [`.grain/templates/nightly.yaml`](../.grain/templates/nightly.yaml), schedule configuration at [`.grain/schedules/nightly.yaml`](../.grain/schedules/nightly.yaml), and prompt definitions stored in [`.grain/prompts/`](../.grain/prompts/).

---

## 2. Nightly Prompt Catalog

The suite is structured into 9 modular agent prompts:

```
.grain/prompts/
├── 01-test-suite-and-quality-gates.md
├── 02-granulefile-conformance-and-compiler.md
├── 03-guest-image-lifecycle-and-rebase.md
├── 04-vm-runtime-networking-and-control-plane.md
├── 05-k8s-and-docker-orchestration.md
├── 06-storage-and-performance-benchmarks.md
├── 07-docs-and-api-spec-consistency.md
├── 08-nightly-regression-triage-and-summary.md
└── 09-daily-new-features-test-plan.md
```

### Summary of Prompt Modules

1. **Test Suite & Quality Gate Verification** (`01-test-suite-and-quality-gates.md`)
   - Complete Go test suite execution (`go test ./...`) with `-race` detection.
   - Kernel networking integration tests under real network namespaces (`GRANULE_NETNS_TESTS=required`).
   - Shell script syntax checks across `deploy/`, `benchmarks/`, `demo/`.

2. **Granulefile Conformance & Compiler** (`02-granulefile-conformance-and-compiler.md`)
   - Rigorous validation of the Granulefile parser and compiler against all worked examples in [The Granule Specification](granulefile.md).
   - Verifies `VM FROM`, `VM PACK`, `VM WRAP`, `VM UNPACK FROM`, `VOLUME`, and multi-stage intermediate translations.

3. **Guest Image Lifecycle & Rebase** (`03-guest-image-lifecycle-and-rebase.md`)
   - Verifies guest rootfs packaging for Debian and Alpine distributions.
   - Validates boot contract invariants, ACPI power-button handlers, and safe runtime rebase without guest rootfs loss.

4. **VM Runtime, Spliced Datapath & Control Plane** (`04-vm-runtime-networking-and-control-plane.md`)
   - Exercises the `granule vm` command surface (`create`, `run`, `exec`, `shell`, `wait`, `status`, `update`, `delete`, `list`).
   - Validates in-kernel `splice()` datapath and vsock-based in-guest agent communication.

5. **Kubernetes & Docker Orchestration** (`05-k8s-and-docker-orchestration.md`)
   - Validates Kubernetes Pod manifests, Multi-VM Pod patterns, and ephemeral debug microVM attachments.
   - Full rehearsal of the presenter demo (`demo/demo.sh --dry-run`).

6. **Unified Performance Benchmarks & Storage Verification** (`06-storage-and-performance-benchmarks.md`)
   - Executes unified benchmark suite (`benchmarks/run_all_benchmarks.sh`) across compute, memory, storage, network, and startup latency.
   - Records benchmark telemetry in `benchmarks/results/` and asserts performance thresholds.

7. **Documentation, API & Spec Consistency Audit** (`07-docs-and-api-spec-consistency.md`)
   - Audits all documentation against active Go CLI flag schemas and specs.
   - Ensures zero stale project name references or invalid code snippets.

8. **Master Regression Triage & Summary Orchestrator** (`08-nightly-regression-triage-and-summary.md`)
   - Orchestrates execution of the entire suite.
   - Distinguishes code regressions from external infrastructure flakes.
   - Reports only what the user needs to address: a green night files nothing and stays silent, clear bugs become `propose_task` follow-ups with `auto_merge: true`, and only issues needing an architectural change or user intervention are reported, with a minimal repro, an architectural impact analysis and specific decision points.

9. **Daily New Features Discovery, Test Plan Design & Autonomous Walkthroughs** (`09-daily-new-features-test-plan.md`)
   - Inspects features and capabilities committed in the preceding 24 hours.
   - Synthesizes targeted test plans, edge-case validations, and end-to-end user journeys.
   - Files follow-up agent tasks via `propose_task` (with `auto_merge: true`) to execute walkthroughs, isolate defects, author in-tree fixes, and auto-merge patches upon green CI.

---

## 3. Deployment Configuration in Grain

The Grain scheduler reads deployment schedules, suites, and template definitions directly from repository declarations in `.grain/schedules/`, `.grain/suites/`, and `.grain/templates/` as well as deployment configuration. The native repository schedule declared in [`.grain/schedules/nightly.yaml`](../.grain/schedules/nightly.yaml) dispatches the `nightly` suite daily at `02:00` UTC with `overlap: skip`.

---

## 4. Automated Suite Orchestration & Triage Runner

Developers and Grain agents can execute the entire continuous verification suite locally or in CI using `test/run-nightly-suite.sh`:

```bash
# Execute all 7 stages with Markdown and JSON report generation:
test/run-nightly-suite.sh --report=/tmp/nightly-report.md --json-report=/tmp/nightly-report.json

# Execute specific stages (e.g. Quality Gates and Compiler Conformance):
test/run-nightly-suite.sh --stages=1,2

# Execute fast dry-run mode:
test/run-nightly-suite.sh --dry-run
```

### Failure Classification & Triage

The runner automatically categorizes failures into:
- `CODE_REGRESSION`: Logic errors, compiler diagnostic regressions, or unit test failures.
- `INFRA_FLAKE`: Missing host virtualization devices (`/dev/kvm`, `/dev/net/tun`) or registry timeouts.
- `TEST_FLAKE`: Concurrency race conditions or non-deterministic timing issues.

---

## 5. IPv6 End-to-End Run

[`test/e2e-ipv6.sh`](../test/e2e-ipv6.sh) boots real granule guests and checks them over IPv6. It covers section 8.3 of [the IPv6 design](ipv6-dual-stack-design.md):

| Target | What it runs | Unattended |
|---|---|---|
| `docker` | a dual-stack network (`--ipv6 --subnet fd00:dead:beef::/64`) and a v6-only one (`--ipv4=false`, docker 27+) | nightly |
| `kind` | `kind-dual` (`networking.ipFamily: dual`) and `kind-ipv6` (`ipFamily: ipv6`), two nodes each | nightly |
| `gke` | a dual-stack (`--stack-type=ipv4-ipv6`) Dataplane V2 cluster in its own VPC, created and deleted by the script | by hand |

Two places run it unattended:

- [`.grain/workflows/e2e-ipv6.yml`](../.grain/workflows/e2e-ipv6.yml) runs `docker kind` every night at 03:30 UTC, after the 02:00 suite. It builds the alpine guest from the tree under test.
- Stage 5 of `test/run-nightly-suite.sh` (step 5.4) runs the same two targets when the host has a usable `/dev/kvm`, docker, kind and kubectl. Otherwise it prints `SKIP` with the reason.

GKE runs only through [`.grain/workflows/e2e-ipv6-gke.yml`](../.grain/workflows/e2e-ipv6-gke.yml), dispatched by hand. A scheduled run gets no GCP key, and the run costs a two-node cluster for about half an hour. GKE has no IPv6-only stack type, so there is no v6-only GKE run.

The docker daemon needs no `"ipv6": true` or `"ip6tables": true` in `daemon.json`. `--ipv6` on a user-defined network is enough, and ip6tables is on by default from docker 27. A daemon that has ip6tables turned off fails the `-p [::]` checks.

Each check prints `PASS`, `FAIL` or `SKIP`. A `SKIP` is something the host cannot test, for example resolving a public name over v6 on a host with no IPv6 default route. It is never a hidden failure.

```bash
GRANULE_IMAGE=my-alpine-guest:dev test/e2e-ipv6.sh docker kind-ipv6
test/e2e-ipv6.sh --keep kind-dual      # leave the cluster up for a look
GKE_EXISTING=1 GKE_NAME=my-cluster test/e2e-ipv6.sh gke   # an existing cluster, left standing
```

