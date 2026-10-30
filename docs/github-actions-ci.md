# GitHub Actions CI (`tests.yml`, `nightly.yml`, `post-reset-e2e.yml`, `kvm-route-stress.yml`, `gcp-smoke.yml`)

This document describes the GitHub Actions ports in `.github/workflows/` of the non-agent test suites defined in `.grain/workflows/`, targeting GitHub-hosted `ubuntu-latest` runners:

- `.github/workflows/tests.yml`: per-PR and per-push suite (`.grain/workflows/tests.yml`)
- `.github/workflows/nightly.yml`: scheduled nightly and on-demand long/soak/stress suite (`.grain/workflows/nightly.yml`)
- `.github/workflows/post-reset-e2e.yml`: scheduled and on-demand post-reset end-to-end qualification suite (`.grain/workflows/post-reset-e2e.yml`)
- `.github/workflows/kvm-route-stress.yml`: on-demand 20-boot two-VM KVM route stress suite (`.grain/workflows/kvm-route-stress.yml`)
- `.github/workflows/gcp-smoke.yml`: scheduled and on-demand GCE VM and GKE cluster lifecycle smoke suite (`.grain/workflows/gcp-smoke.yml`)

## Triggers, Concurrency, and Permissions

- **Triggers (`on:`)**:
  - `tests.yml`: `push` on `main`, `pull_request`, `merge_group`, and `workflow_dispatch`
  - `nightly.yml`: `schedule` (`cron: "11 2 * * *"`) and `workflow_dispatch`
  - `post-reset-e2e.yml`: `schedule` (`cron: "23 5 * * *"`) and `workflow_dispatch`
  - `kvm-route-stress.yml`: `workflow_dispatch`
  - `gcp-smoke.yml`: `schedule` (`cron: "41 4 * * *"`) and `workflow_dispatch`
- **Concurrency (`concurrency:`)**:
  - Every job defines its own concurrency group keyed on `${{ github.workflow }}-<job-id>-${{ github.ref }}` with `cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}`, so superseded pushes to an open PR cancel in-flight runs for that job and ref without cancelling sibling jobs in the same workflow run or `main` branch runs.
- **Timeouts (`timeout-minutes:`)**:
  - Every job sets an explicit `timeout-minutes` bound (`15` to `110` minutes depending on suite duration on 4-vCPU `ubuntu-latest` runners).
- **Permissions (`permissions:`)**:
  - Top-level `contents: read` (`actions/checkout` runs with `persist-credentials: false`).
  - `gcp-smoke.yml` additionally grants `id-token: write` so `google-github-actions/auth` can exchange a GitHub Actions OIDC token via Workload Identity Federation.
  - All third-party actions are pinned to full 40-character commit SHAs.

## Job-by-Job Mapping

### 1. Per-PR Suite (`.grain/workflows/tests.yml` -> `.github/workflows/tests.yml`)

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
| `real-kubernetes` | `real kubernetes` | Boots a `kind` + Calico cluster and exercises Kubernetes pod sandboxes | Present in `.github/workflows/tests.yml` behind `if: vars.ENABLE_SELF_HOSTED_TESTS == 'true'` and `runs-on: ${{ vars.SELF_HOSTED_RUNNER \|\| 'ubuntu-latest' }}` | Disabled by default (self-hosted target) |

### 2. Nightly Suite (`.grain/workflows/nightly.yml` -> `.github/workflows/nightly.yml`)

Schedule: `cron: "11 2 * * *"` and `workflow_dispatch`.

| Job ID | Display Name | Grain CI (`.grain/workflows/nightly.yml`) | GitHub Actions (`.github/workflows/nightly.yml`) | Hosted Runner Status |
| :--- | :--- | :--- | :--- | :--- |
| `go-long` | `go, shuffled and repeated` | Runs `make test-long` (`make frontend` + `go test -race -shuffle=on -count=2 -timeout 90m ./...`) | Same commands on `ubuntu-latest` with `fetch-depth: 0`, Go/Node setup, caches, `bubblewrap`, system `postgresql` stopped, and `git config` | Active (`ubuntu-latest`) |
| `staterepo-soak` | `state repository soak` | Runs `make soak` (2,000 rounds of `TestSoak` in `./pkg/staterepo/`) | Same commands on `ubuntu-latest` with Go setup, cache, and `git config` | Active (`ubuntu-latest`) |
| `loadtest` | `sustained load` | Runs `make loadtest` (`make frontend` + `TestLoadSustainedConcurrency` in `./tests/e2e/`) | Same commands on `ubuntu-latest` with Go/Node setup, caches, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `scalability` | `metadata and model scalability` | Runs `make scalability` (`make frontend` + `TestScalabilityLimits` in `./tests/e2e/`) | Same commands on `ubuntu-latest` with Go/Node setup, caches, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `merge-stress` | `merge liveness stress` | Runs `make merge-stress` (`TestMergeStressSustained` in `./tests/e2e/`) | Same commands on `ubuntu-latest` with Go setup, cache, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `long-term-test` | `end-to-end long term tests` | Runs `make long-term-test` (`TestLongTermEndToEndSustained` in `./tests/e2e/`) | Same commands on `ubuntu-latest` with Go setup, cache, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `long-sim` | `long randomized cluster simulation` | Runs `make long-sim` (20-minute `TestRandomizedClusterLong` in `./tests/e2e/`) | Same commands on `ubuntu-latest` with Go setup, cache, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `postgres-stress` | `postgres concurrency and stress` | Installs `postgresql` binaries and runs `GRAIN_REQUIRE_POSTGRES=1 make postgres-stress` (`./pkg/model/postgres/... -run TestPostgres`) | Declares `services: postgres` (`postgres:16-bookworm`), stops default system cluster on port 5432, ensures `postgresql` binaries are present, and runs `GRAIN_REQUIRE_POSTGRES=1 make postgres-stress` | Active (`ubuntu-latest`) |
| `tunnel-stress` | `reverse tunnel multiplexer stress` | Runs `make tunnel-stress` (30-minute `./pkg/tunnel/...` stress suite) | Same commands on `ubuntu-latest` with Go setup and cache | Active (`ubuntu-latest`) |
| `ui-e2e-long` | `ui e2e, repeated` | Runs `make test-e2e-long` (3 full passes of Chromium + WebKit Playwright e2e suite) and uploads `ui/playwright-report` + `ui/test-results` | Same commands on `ubuntu-latest` with third-party apt list cleanup, `/var/cache/playwright` caching, and `actions/upload-artifact` on failure | Active (`ubuntu-latest`) |
| `installer-variants` | `installer trixie and hostmode` | Assembles `grain-installer:test` (`GRAIN_INSTALLER_SANDBOX_IMAGE=debian:trixie`) and runs `./tests/installer/trixie` + `./tests/installer/hostmode` | Same commands on `ubuntu-latest` with `fetch-depth: 0` (required by `scripts/export-source.sh`), Go/Node setup, caches, and `git config` | Active (`ubuntu-latest`) |
| `security-audit` | `Security suite and audit` | Runs `make security` (`go vet ./...` + `go test -race -count=1 ./tests/security/...`) | Same commands on `ubuntu-latest` with Go setup and cache | Active (`ubuntu-latest`) |
| `vulns` | `govulncheck` | Runs `make vuln` (`govulncheck ./...` under `go.mod` toolchain) | Same commands on `ubuntu-latest` with Go setup and cache | Active (`ubuntu-latest`) |
| `cmd-grain-stress` | `pkg/cli stress under load` | Runs 4-way parallel `go test -race -count=1 ./pkg/cli` rounds within a 1,800s budget | Same stress loop on `ubuntu-latest` with Go setup, cache, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `kvm-sandboxes` | `real KVM sandboxes` | Resolves guest image (`scripts/granule-guest-resolve.sh`) and runs real KVM VM tests in `./pkg/orchestrator/` | Present in `.github/workflows/nightly.yml` behind `if: vars.ENABLE_SELF_HOSTED_TESTS == 'true'` and `runs-on: ${{ vars.SELF_HOSTED_RUNNER \|\| 'ubuntu-latest' }}` | Disabled by default (self-hosted target) |
| `kvm-route-stress` | `twenty two-VM boots` | Resolves guest image and runs 20 consecutive boots of `TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently` | Present in `.github/workflows/nightly.yml` behind `if: vars.ENABLE_SELF_HOSTED_TESTS == 'true'` and `runs-on: ${{ vars.SELF_HOSTED_RUNNER \|\| 'ubuntu-latest' }}` | Disabled by default (self-hosted target) |
| `upgrade-test` | `upgrade from previous version` | Builds `grain-e2e:test` (`scripts/ci-docker-build.sh`) and runs `make test-upgrade` (`./tests/container/ -run 'TestUpgradeFromPreviousVersion\|TestAnUpgrade'`) | Same commands on `ubuntu-latest` with `fetch-depth: 0`, Go setup, cache, and `git config` | Active (`ubuntu-latest`) |

### 3. Post-Reset End-to-End Qualification (`.grain/workflows/post-reset-e2e.yml` -> `.github/workflows/post-reset-e2e.yml`)

Schedule: `cron: "23 5 * * *"` and `workflow_dispatch`.

| Job ID | Display Name | Grain CI (`.grain/workflows/post-reset-e2e.yml`) | GitHub Actions (`.github/workflows/post-reset-e2e.yml`) | Hosted Runner Status |
| :--- | :--- | :--- | :--- | :--- |
| `deployments` | `deployments (SQLite and Postgres)` | Installs PostgreSQL binaries, builds `grain-core` and `grain-v1.13.0` worktree binary, and runs `./scripts/post-reset-e2e.sh` for `sqlite` and `postgres` | Same commands on `ubuntu-latest` with `fetch-depth: 0` (for tag `v1.13.0`), Go setup, cache, system `postgresql` stopped, and `git config` | Active (`ubuntu-latest`) |
| `harness` | `e2e harness (githubsim)` | Runs `go test -count=1 -timeout 25m ./tests/e2e/` | Same commands on `ubuntu-latest` with Go/Node setup, caches, `bubblewrap`, `git config`, and `make frontend` | Active (`ubuntu-latest`) |
| `security` | `route authorization` | Runs `go test -count=1 -timeout 15m ./tests/security/...` | Same commands on `ubuntu-latest` with Go setup and cache | Active (`ubuntu-latest`) |

### 4. KVM Route Stress (`.grain/workflows/kvm-route-stress.yml` -> `.github/workflows/kvm-route-stress.yml`)

Trigger: `workflow_dispatch`.

| Job ID | Display Name | Grain CI (`.grain/workflows/kvm-route-stress.yml`) | GitHub Actions (`.github/workflows/kvm-route-stress.yml`) | Hosted Runner Status |
| :--- | :--- | :--- | :--- | :--- |
| `kvm-route-stress` | `twenty two-VM boots` | Resolves `grain-guest:e2e-test` via `scripts/granule-guest-resolve.sh` and runs `TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently` with `-count=20` | Present in `.github/workflows/kvm-route-stress.yml` behind `if: vars.ENABLE_SELF_HOSTED_TESTS == 'true'` and `runs-on: ${{ vars.SELF_HOSTED_RUNNER \|\| 'ubuntu-latest' }}` | Disabled by default (self-hosted target) |

### 5. GCP Smoke (`.grain/workflows/gcp-smoke.yml` -> `.github/workflows/gcp-smoke.yml`)

Schedule: `cron: "41 4 * * *"` and `workflow_dispatch`.

| Job ID | Display Name | Grain CI (`.grain/workflows/gcp-smoke.yml`) | GitHub Actions (`.github/workflows/gcp-smoke.yml`) | Hosted Runner Status |
| :--- | :--- | :--- | :--- | :--- |
| `smoke` | `GCE and GKE lifecycle` | Activates `/home/debian/.gcp-service-account.json` (`https://grain.dev/capabilities/gcp#gcp-key`), installs `kubectl` + `gke-gcloud-auth-plugin`, runs `scripts/gce-vm-smoke.sh --iap` and `scripts/gke-cluster-smoke.sh`, and sweeps stale resources | Checks `vars.GCP_WIF_PROVIDER` and `vars.GCP_SA_EMAIL` (skips with a `::notice` if unset); otherwise authenticates via `google-github-actions/auth` (Workload Identity Federation) and runs the same GCE/GKE smoke and cleanup steps | Active (`ubuntu-latest`; skips cleanly when WIF variables are unset) |

## What Was Emulated from the Grain Sandbox Image (`Granulefile`)

Grain CI runs jobs inside a pre-provisioned VM/container image built from `Granulefile` and `Granulefile.guest-base`. Stock GitHub-hosted `ubuntu-latest` runners differ in several ways that `.github/workflows/*.yml` emulate explicitly:

1. **Grain Dialect Keys Removed**:
   - Grain-only keys (`capabilities:`, `optional-capabilities:`, `sandbox:`, `infrastructure:`, `guest-cache:`, and job-level `paths:` / `paths-ignore:` / `artifacts:`) are removed or translated to standard GitHub Actions equivalents (`actions/upload-artifact`).
2. **Checkout and Toolchains (`actions/checkout`, `actions/setup-go`, `actions/setup-node`)**:
   - Grain sandboxes start with the repository pre-checked-out and Go `1.26.10` + Node.js `20` pre-installed.
   - Every GitHub Actions job checks out the repository with `actions/checkout` (`persist-credentials: false`; jobs that invoke `scripts/export-source.sh` or inspect git tags/history pass `fetch-depth: 0`), installs Go from `go.mod`'s `toolchain` directive (`go1.26.10`) via `actions/setup-go`, and installs Node.js `20` via `actions/setup-node` where needed.
   - Jobs that run Go tests launching `git` subprocesses with scrubbed environments configure system and global `git config user.name` and `user.email`.
3. **Explicit Caching (`actions/cache`)**:
   - Go jobs cache `~/go/pkg/mod` and `~/.cache/go-build` keyed on `hashFiles('go.sum', 'third_party/granule/go.sum')`.
   - Frontend/UI jobs cache `~/.npm` and `ui/node_modules` keyed on `hashFiles('ui/package-lock.json')`.
   - `ui-e2e` and `ui-e2e-long` cache `/var/cache/playwright` keyed on `hashFiles('ui/package-lock.json')` after creating `/var/cache/playwright` owned by `$USER`.
4. **Bubblewrap (`bwrap`) and Ubuntu 24.04 AppArmor User Namespaces**:
   - Confinement tests in `pkg/mcp`, `pkg/daemon`, and `pkg/orchestrator` (`host_confine_test.go`, `host_sandbox_hide_test.go`) require `bwrap` to create unprivileged user namespaces when `bwrap` is installed.
   - Ubuntu 24.04 (`ubuntu-latest`) sets `kernel.apparmor_restrict_unprivileged_userns=1` by default, blocking `bwrap` from creating user namespaces. Go test jobs install `bubblewrap` alongside `expect`, `zstd`, `socat`, and `psmisc` and set `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`.
5. **PostgreSQL (`services: postgres` + Local Binaries)**:
   - `postgres` (`tests.yml`) and `postgres-stress` (`nightly.yml`) declare a `postgres:16-bookworm` service container (mapped to `5433:5432` with `pg_isready` health checks and tmpfs storage).
   - `postgres`, `postgres-stress`, `go-long`, and `post-reset-e2e`'s `deployments` stop and disable the runner's default system `postgresql` service so port `5432` remains free for `postgres.EnsureRunning` and `postgrestest` (which tune `max_connections = 200` for high-concurrency parallel store tests).
6. **`/dev/kvm` udev Rule (`granule`, `kvm-sandboxes`, `kvm-route-stress`)**:
   - Jobs that access `/dev/kvm` write `/etc/udev/rules.d/99-kvm4all.rules` (`KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"`), reload udev rules, and apply `chmod 0666 /dev/kvm` when `/dev/kvm` is present on the runner.
7. **Docker Daemon Bridge & Containerd Snapshotter Configuration (`package-cache`)**:
   - `TestPackageCachePullsThroughARealDockerd` verifies that `orchestrator.ConfigurePackageCache` preserves the sandbox's existing `/etc/docker/daemon.json` settings (`bip: "10.240.0.1/24"` and `default-address-pools: [{"base": "10.241.0.0/16", "size": 24}]` from `Granulefile`) and pulls images through `registry-mirrors` (`docker.io`) as well as `/etc/docker/certs.d/{ghcr.io,gcr.io}/hosts.toml`.
   - Stock `ubuntu-latest` runners do not include those keys in `/etc/docker/daemon.json`. The `package-cache` job seeds `/etc/docker/daemon.json` with `Granulefile`'s configuration plus `"features": {"containerd-snapshotter": true}` (so `dockerd` honors `certs.d/*/hosts.toml` mirrors) and restarts `docker.service` before running the package-cache and transparent-interception tests.
8. **Playwright & APT Third-Party Sources (`ui-e2e`, `ui-e2e-long`, `gcp-smoke`)**:
   - `ubuntu-latest` includes third-party apt lists (`/etc/apt/sources.list.d/google-chrome.list`, `/etc/apt/sources.list.d/microsoft-prod.list`) whose signing keys can cause `apt-get update` to fail. Jobs that run `apt-get update` or `make playwright-browsers` remove those unused lists first.

## Jobs Gated for Self-Hosted Runners (`vars.ENABLE_SELF_HOSTED_TESTS`)

Heavyweight nested-VM and multi-node Kubernetes suites are included in `.github/workflows/tests.yml`, `.github/workflows/nightly.yml`, and `.github/workflows/kvm-route-stress.yml` behind `if: vars.ENABLE_SELF_HOSTED_TESTS == 'true'` and `runs-on: ${{ vars.SELF_HOSTED_RUNNER || 'ubuntu-latest' }}` so they are documented in-place and ready to enable once self-hosted runners (or pre-published guest VM images in GHCR) are configured:

1. **`real-vm` (`tests.yml`), `kvm-sandboxes` (`nightly.yml`), and `kvm-route-stress` (`nightly.yml` and `kvm-route-stress.yml`)**:
   - **Why they are disabled by default on `ubuntu-latest`**: Each boots concurrent Firecracker / Cloud-Hypervisor microVMs (`Granulefile` configures 4 vCPUs and 4,096 MiB RAM per VM) inside Docker containers (`TestGranuleSandboxesAgainstARealDockerBackedVM`, `TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently`, `TestTheModelProxiesAreReachableFromInsideARealGuest`, `TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM`, `TestHostScriptGrantInARealGranuleVM`). `scripts/granule-guest-resolve.sh` requires a pre-published microVM guest image in GHCR matching `./scripts/granule/guest-inputs.sh` (or a host-level Docker cache); on a cold cache miss, building `Granulefile` inline takes 50–110 minutes and >30 GB of free disk space, exceeding standard hosted runner disk budgets (~14–20 GB free).
2. **`real-kubernetes` (`tests.yml`)**:
   - **Why it is disabled by default on `ubuntu-latest`**: Provisions a multi-node `kind` cluster with Calico CNI to test Kubernetes NetworkPolicy egress isolation and real pod sandboxes, exceeding standard hosted runner memory and disk budgets.

## Required Repository Variables and Secrets

- **Secrets required**: **None**.
  - All suites authenticate either anonymously (public GHCR images) or via keyless OIDC Workload Identity Federation (`gcp-smoke.yml`).
- **Repository variables (`Settings -> Secrets and variables -> Actions -> Variables`)**:
  - `GCP_WIF_PROVIDER` *(required to run `gcp-smoke.yml`, optional otherwise)*: Full resource name of the Google Cloud Workload Identity Provider (e.g. `projects/123456789/locations/global/workloadIdentityPools/github-pool/providers/github-provider`). When unset, `gcp-smoke.yml` emits a `::notice` annotation and skips cleanly.
  - `GCP_SA_EMAIL` *(required to run `gcp-smoke.yml`, optional otherwise)*: Email address of the GCP service account to impersonate for GCE/GKE lifecycle smoke tests (e.g. `grain-smoke@<project>.iam.gserviceaccount.com`). When unset, `gcp-smoke.yml` emits a `::notice` annotation and skips cleanly.
  - `GCP_PROJECT_ID` *(optional, for `gcp-smoke.yml`)*: Explicit GCP project ID to configure in `gcloud`; if omitted, `gcp-smoke.yml` derives the project ID from `GCP_SA_EMAIL`.
  - `ENABLE_SELF_HOSTED_TESTS` *(optional)*: Set to `'true'` to enable the self-hosted nested-VM and Kubernetes jobs (`real-vm`, `real-kubernetes`, `kvm-sandboxes`, `kvm-route-stress`).
  - `SELF_HOSTED_RUNNER` *(optional)*: Runner label for self-hosted jobs (defaults to `'ubuntu-latest'` when unset).
