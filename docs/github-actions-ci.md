# GitHub Actions CI (`tests.yml`)

This document describes `.github/workflows/tests.yml`, the GitHub Actions port of the per-PR and per-push test suite defined in `.grain/workflows/tests.yml`, targeting GitHub-hosted `ubuntu-latest` runners.

## Triggers, Concurrency, and Permissions

- **Triggers (`on:`)**:
  - `push` on `main`
  - `pull_request`
  - `merge_group` (added for GitHub merge queue compatibility)
  - `workflow_dispatch`
- **Concurrency (`concurrency:`)**:
  - Every job defines its own concurrency group keyed on `${{ github.workflow }}-<job-id>-${{ github.ref }}` with `cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}`, so superseded pushes to an open PR cancel in-flight runs for that job and ref without cancelling sibling jobs in the same workflow run or `main` branch runs.
- **Timeouts (`timeout-minutes:`)**:
  - Every job sets an explicit `timeout-minutes` bound (`15` to `35` minutes depending on shard weight on 4-vCPU `ubuntu-latest` runners).
- **Permissions (`permissions:`)**:
  - Top-level `contents: read` (least-privilege token; `actions/checkout` runs with `persist-credentials: false`).
  - All third-party actions are pinned to full 40-character commit SHAs.

## Job-by-Job Mapping (`.grain/workflows/tests.yml` -> `.github/workflows/tests.yml`)

| Job ID | Display Name | Grain CI (`.grain/workflows/tests.yml`) | GitHub Actions (`.github/workflows/tests.yml`) | Hosted Runner Status |
| :--- | :--- | :--- | :--- | :--- |
| `go` | `go` | Runs `make fmt`, `make frontend`, `go build ./...`, `go vet ./...`, and `-race` tests for `./pkg/ui/... ./cmd/grain/... ./ui/e2e/...` | Same commands on `ubuntu-latest` with `actions/setup-go`, `actions/setup-node`, `actions/cache`, and `bubblewrap` setup | Active (`ubuntu-latest`) |
| `go-e2e` | `go e2e` | Builds frontend (`make frontend`) and runs `-race` tests for `./tests/e2e/... ./tests/deploy/...` | Same commands on `ubuntu-latest` with Go/Node setup, caches, `bubblewrap`, and `shellcheck` | Active (`ubuntu-latest`) |
| `go-orchestrator` | `go orchestrator` | Runs `-race` tests for `./pkg/orchestrator/...` | Same commands on `ubuntu-latest` with Go setup, cache, and `bubblewrap` + network tools | Active (`ubuntu-latest`) |
| `go-model` | `go model` | Runs `-race` tests for `./pkg/model/... ./pkg/staterepo/... ./tests/security/...` | Same commands on `ubuntu-latest` with Go setup and cache | Active (`ubuntu-latest`) |
| `go-unit` | `go unit` | Runs `-race` tests across all remaining CLI, daemon, agent, MCP, and utility packages | Same package list and commands on `ubuntu-latest` with Go setup, cache, and `bubblewrap` | Active (`ubuntu-latest`) |
| `granule` | `granule` | Runs `-race` tests for `third_party/granule` (`-mod=vendor`) | Same commands on `ubuntu-latest` plus `/dev/kvm` udev rule setup and network utilities | Active (`ubuntu-latest`) |
| `postgres` | `postgres` | Installs `postgresql` binaries (`policy-rc.d` inhibits system service) and runs `./pkg/model/postgres/... ./pkg/model/` with `GRAIN_REQUIRE_POSTGRES=1` | Declares `services: postgres` (`postgres:16-bookworm`) and installs `postgresql` binaries for `postgrestest`/`EnsureRunning` | Active (`ubuntu-latest`) |
| `run-host` | `run host` | Pulls `debian:bookworm-slim` and runs `pkg/runhost` + `TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest` (`GRAIN_RUNHOST_DOCKER=1`) | Same commands against the runner's Docker daemon | Active (`ubuntu-latest`) |
| `host-script-container` | `host-script container` | Builds `Dockerfile.k8s-sandbox` (without agent CLIs) and runs `TestHostScriptGrantInARealContainerSandbox` | Same commands against the runner's Docker daemon | Active (`ubuntu-latest`) |
| `frontend` | `frontend` | Runs `make frontend`, `make fmt-frontend`, and `npm test -- --maxWorkers=4` | Same commands on `ubuntu-latest` with `actions/setup-node` and npm/`ui/node_modules` caching | Active (`ubuntu-latest`) |
| `ui-e2e` | `ui e2e` | Builds frontend, installs Playwright Chromium (`make playwright-browsers`), runs `make test-e2e-suite`, collects `ui/test-results` | Same commands on `ubuntu-latest` with third-party apt source cleanup, `/var/cache/playwright` caching, and `actions/upload-artifact` on failure | Active (`ubuntu-latest`) |
| `lint` | `lint` | Runs `shellcheck` over `git ls-files -- 'scripts/*.sh' 'deploy/terraform/*.sh'` | Runs `actionlint` over `.github/workflows/` and `shellcheck` over tracked shell scripts | Active (`ubuntu-latest`) |
| `terraform` | `terraform` | Runs `terraform fmt -check`, `init -backend=false`, and `validate` across `deploy/terraform/gcp` and `deploy/gke/terraform` | Pins Terraform `1.9.8` via `hashicorp/setup-terraform` and runs the same format/init/validate steps | Active (`ubuntu-latest`) |
| `installer-e2e` | `installer` | Assembles `grain-installer:test` via `scripts/installer-test-image.sh` and runs `./tests/installer` (`GRAIN_INSTALLER_E2E=1`) | Same commands on `ubuntu-latest` (uses anonymous GHCR pulls from `ghcr.io/bwsalmon/grain/*` and host systemd/Docker) | Active (`ubuntu-latest`) |
| `package-cache` | `package cache` | Runs `pkg/orchestrator` real-dockerd cache tests (`GRAIN_REAL_DOCKER_CACHE_TESTS=1`) and `pkg/daemon` transparent interception test (`GRAIN_REAL_INTERCEPT_TESTS=1`) | Seeds `/etc/docker/daemon.json` with `Granulefile`'s `bip`/`default-address-pools`/`mtu`, restarts Docker, and runs the same tests | Active (`ubuntu-latest`) |
| `real-vm` | `real vm` | Formerly in `tests.yml` (now in `nightly.yml` as `kvm-sandboxes`): boots real Firecracker/Cloud-Hypervisor VMs | Present in `.github/workflows/tests.yml` behind `if: vars.ENABLE_SELF_HOSTED_TESTS == 'true'` and `runs-on: ${{ vars.SELF_HOSTED_RUNNER \|\| 'ubuntu-latest' }}` | Disabled by default (self-hosted target) |
| `real-kubernetes` | `real kubernetes` | Runs in `nightly.yml`: boots a `kind` + Calico cluster and exercises Kubernetes pod sandboxes | Present in `.github/workflows/tests.yml` behind `if: vars.ENABLE_SELF_HOSTED_TESTS == 'true'` and `runs-on: ${{ vars.SELF_HOSTED_RUNNER \|\| 'ubuntu-latest' }}` | Disabled by default (self-hosted target) |

## What Was Emulated from the Grain Sandbox Image (`Granulefile`)

Grain CI runs jobs inside a pre-provisioned VM/container image built from `Granulefile` and `Granulefile.guest-base`. Stock GitHub-hosted `ubuntu-latest` runners differ in several ways that `.github/workflows/tests.yml` emulates explicitly:

1. **Grain Dialect Keys Removed**:
   - Grain-only keys (`capabilities:`, `optional-capabilities:`, `sandbox:`, `infrastructure:`, and job-level `paths:` / `paths-ignore:` / `artifacts:`) are removed or translated to standard GitHub Actions equivalents (`actions/upload-artifact`).
2. **Checkout and Toolchains (`actions/checkout`, `actions/setup-go`, `actions/setup-node`)**:
   - Grain sandboxes start with the repository pre-checked-out and Go `1.26.10` + Node.js `20` pre-installed.
   - Every GitHub Actions job checks out the repository with `actions/checkout` (`persist-credentials: false`), installs Go from `go.mod`'s `toolchain` directive (`go1.26.10`) via `actions/setup-go`, and installs Node.js `20` via `actions/setup-node` where needed.
3. **Explicit Caching (`actions/cache`)**:
   - Go jobs cache `~/go/pkg/mod` and `~/.cache/go-build` keyed on `hashFiles('go.sum', 'third_party/granule/go.sum')`.
   - Frontend/UI jobs cache `~/.npm` and `ui/node_modules` keyed on `hashFiles('ui/package-lock.json')`.
   - `ui-e2e` caches `/var/cache/playwright` keyed on `hashFiles('ui/package-lock.json')` after creating `/var/cache/playwright` owned by `$USER`.
4. **Bubblewrap (`bwrap`) and Ubuntu 24.04 AppArmor User Namespaces**:
   - Confinement tests in `pkg/mcp`, `pkg/daemon`, and `pkg/orchestrator` (`host_confine_test.go`, `host_sandbox_hide_test.go`) require `bwrap` to create unprivileged user namespaces when `bwrap` is installed.
   - Ubuntu 24.04 (`ubuntu-latest`) sets `kernel.apparmor_restrict_unprivileged_userns=1` by default, blocking `bwrap` from creating user namespaces. Go test shards install `bubblewrap` alongside `expect`, `zstd`, `socat`, and `psmisc` and set `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`.
5. **PostgreSQL (`services: postgres` + Local Binaries)**:
   - The `postgres` job declares a `postgres:16-bookworm` service container (mapped to `5433:5432` with `pg_isready` health checks and tmpfs storage) and ensures the `postgresql` server binaries (`initdb`, `pg_ctl`, `postgres` under `/usr/lib/postgresql/*/bin/`) are installed with the default system cluster stopped so port `5432` remains free for `postgres.EnsureRunning` and `postgrestest` (which tune `max_connections = 200` for high-concurrency parallel store tests).
6. **`/dev/kvm` udev Rule (`granule`)**:
   - The `granule` job writes `/etc/udev/rules.d/99-kvm4all.rules` (`KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"`), reloads udev rules, and applies `chmod 0666 /dev/kvm` when `/dev/kvm` is present on the runner.
7. **Docker Daemon Bridge & Containerd Snapshotter Configuration (`package-cache`)**:
   - `TestPackageCachePullsThroughARealDockerd` verifies that `orchestrator.ConfigurePackageCache` preserves the sandbox's existing `/etc/docker/daemon.json` settings (`bip: "10.240.0.1/24"` and `default-address-pools: [{"base": "10.241.0.0/16", "size": 24}]` from `Granulefile`) and pulls images through `registry-mirrors` (`docker.io`) as well as `/etc/docker/certs.d/{ghcr.io,gcr.io}/hosts.toml`.
   - Stock `ubuntu-latest` runners do not include those keys in `/etc/docker/daemon.json`. The `package-cache` job seeds `/etc/docker/daemon.json` with `Granulefile`'s configuration plus `"features": {"containerd-snapshotter": true}` (so `dockerd` honors `certs.d/*/hosts.toml` mirrors) and restarts `docker.service` before running the package-cache and transparent-interception tests.
8. **Playwright Third-Party APT Sources (`ui-e2e`)**:
   - `ubuntu-latest` includes third-party apt lists (`/etc/apt/sources.list.d/google-chrome.list`, `/etc/apt/sources.list.d/microsoft-prod.list`) whose signing keys can cause `npx playwright install --with-deps chromium`'s `apt-get update` to fail. The `ui-e2e` job removes those unused lists before invoking `make playwright-browsers`.

## Jobs Gated for Self-Hosted Runners (`vars.ENABLE_SELF_HOSTED_TESTS`)

Two heavyweight infrastructure suites are included in `.github/workflows/tests.yml` behind `if: vars.ENABLE_SELF_HOSTED_TESTS == 'true'` and `runs-on: ${{ vars.SELF_HOSTED_RUNNER || 'ubuntu-latest' }}` so they are documented in-place and ready to enable once self-hosted runners are configured:

1. **`real-vm`**:
   - **Why it is disabled on `ubuntu-latest`**: Boots multiple concurrent Firecracker / Cloud-Hypervisor microVMs inside Docker (`TestGranuleSandboxesAgainstARealDockerBackedVM`, `TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently`, `TestTheModelProxiesAreReachableFromInsideARealGuest`). Requires a pre-published microVM guest image in GHCR matching `./scripts/granule/guest-inputs.sh` (or ~45 minutes and >30 GB disk to build `Granulefile` inline) plus >=16 GiB RAM and 60 GB free disk.
2. **`real-kubernetes`**:
   - **Why it is disabled on `ubuntu-latest`**: Provisions a multi-node `kind` cluster with Calico CNI to test Kubernetes NetworkPolicy egress isolation and real pod sandboxes, exceeding standard hosted runner memory and disk budgets.

## Required Repository Settings and Secrets

- **Secrets required**: **None**.
  - All tests in `.github/workflows/tests.yml` are completely self-contained and credential-free.
  - `installer-e2e` pulls `ghcr.io/bwsalmon/grain/grain:main` and `ghcr.io/bwsalmon/grain/k8s-sandbox:main` anonymously.
  - `terraform` runs `terraform init -backend=false`, which downloads provider schemas without contacting GCS or requiring GCP credentials.
- **Repository settings**:
  - Standard GitHub Actions enabled with default `GITHUB_TOKEN` read permissions (`contents: read`).
