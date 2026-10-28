# Granule Nightly Agent Test Suite

This directory contains the suite of automated agent prompts executed nightly by Grain to test, validate, and benchmark the `gke-labs/granule` codebase.

## Suite Overview

The nightly suite is configured in [`.grain/suites/nightly.yaml`](../suites/nightly.yaml), with task templates in [`.grain/templates/nightly.yaml`](../templates/nightly.yaml), and executes on a daily schedule (`02:00 UTC`). It covers end-to-end repository health, compiler compliance, guest image lifecycle, microVM runtime, Kubernetes/Docker orchestration, performance benchmarks, and documentation integrity.

### Prompt Catalog

| Prompt File | Test Scope & Objective | Criticality |
| :--- | :--- | :--- |
| [`01-test-suite-and-quality-gates.md`](./01-test-suite-and-quality-gates.md) | Full Go unit tests (`go test ./...`), race detection (`-race`), static analysis (`go vet`, `gofmt`), and kernel networking tests (`GRANULE_NETNS_TESTS=required`). | **Critical** |
| [`02-granulefile-conformance-and-compiler.md`](./02-granulefile-conformance-and-compiler.md) | Granulefile compiler validation across all language specifications (§14.1-§14.9), `VM FROM`, `VM PACK`, `VM WRAP`, `VM UNPACK FROM`, `VOLUME`, and multi-stage builds. | **Critical** |
| [`03-guest-image-lifecycle-and-rebase.md`](./03-guest-image-lifecycle-and-rebase.md) | Guest OS overlays (Debian, Alpine), boot contracts, image packing (`guest pack`), rebase safety (`guest rebase`), and publish script dry-run. | **Critical** |
| [`04-vm-runtime-networking-and-control-plane.md`](./04-vm-runtime-networking-and-control-plane.md) | MicroVM lifecycle CLI (`granule vm`), Cloud-Hypervisor integration, spliced networking (`tap0` ↔ `veth`), vsock control plane, and memory agent. | **Critical** |
| [`05-k8s-and-docker-orchestration.md`](./05-k8s-and-docker-orchestration.md) | Kubernetes Pod manifests, Multi-VM Pods (1:1 container-to-VM), ephemeral debug containers (`kubectl debug`), Docker runs, and demo rehearsal (`demo.sh`). | Standard |
| [`06-storage-and-performance-benchmarks.md`](./06-storage-and-performance-benchmarks.md) | Unified benchmark suite (`run_all_benchmarks.sh`), performance telemetry in `benchmarks/results/`, sub-100ms boot latency, and QCow2 layer efficiency. | Standard |
| [`07-docs-and-api-spec-consistency.md`](./07-docs-and-api-spec-consistency.md) | Documentation audit verifying CLI commands, flag signatures, code snippets, and ensuring zero obsolete project references. | Standard |
| [`08-nightly-regression-triage-and-summary.md`](./08-nightly-regression-triage-and-summary.md) | Master orchestrator prompt aggregating nightly results, triaging regressions, classifying root causes, and filing clear bugs as auto-merge follow-up tasks; reports only issues the user must address. | **Critical** |
| [`09-daily-new-features-test-plan.md`](./09-daily-new-features-test-plan.md) | Daily feature discovery, walkthrough test plan design, and automated follow-up task dispatch (`propose_task` with auto-merge) for in-tree validation and bug fixing. | **Critical** |

## Reporting Policy

The nightly prompts report only what the user needs to address:
- **Everything is fine**: file nothing and do not report. A green night is silent.
- **Clear bugs**: file follow-up tasks via `propose_task` with `auto_merge: true` and do not report them.
- **Otherwise** (an architectural change or user intervention is needed): report to the user with a minimal repro, an analysis of the architectural impact, and specific decision points.

Prompts 01, 08 and 09 spell this out, and `pkg/cli/prompts_test.go` pins it.

## Running Nightly Prompts Manually

Developers and operators can execute the automated runner or individual prompts:

```bash
# Execute the full suite in sequence and generate triage reports:
test/run-nightly-suite.sh --report=/tmp/nightly-report.md --json-report=/tmp/nightly-report.json

# Or execute commands manually:
go test -v ./...
sudo env "PATH=$PATH" GRANULE_NETNS_TESTS=required go test -count=1 ./pkg/netshim/...
demo/demo.sh --dry-run --no-color
deploy/publish-images.sh --dry-run --no-login all
find deploy benchmarks demo -name "*.sh" -exec bash -n {} +
```

## Adding New Prompts

When introducing major new features or architectural components:
1. Create a new prompt file in `.grain/prompts/` following the existing format (`Objective`, `Context & Prerequisites`, `Execution Steps`, `Evaluation & Reporting Criteria`).
2. Register the prompt template in `.grain/templates/` and add to the suite in `.grain/suites/nightly.yaml`.
3. Update this `README.md` and `docs/nightly-test-suite.md`.
