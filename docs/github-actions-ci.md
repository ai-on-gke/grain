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

## Required Repository Settings and Secrets (`tests.yml`)

- **Secrets required for `tests.yml`**: **None**.
  - All tests in `.github/workflows/tests.yml` are completely self-contained and credential-free.
  - `installer-e2e` pulls `ghcr.io/bwsalmon/grain/grain:main` and `ghcr.io/bwsalmon/grain/k8s-sandbox:main` anonymously.
  - `terraform` runs `terraform init -backend=false`, which downloads provider schemas without contacting GCS or requiring GCP credentials.
- **Repository settings**:
  - Standard GitHub Actions enabled with default `GITHUB_TOKEN` read permissions (`contents: read`).

---

# Release and Image Publishing Workflows (`image.yml`, `release.yml`, `promote.yml`, `release-train.yml`, `promote-release.yml`)

The five release and image-building workflows under `.grain/workflows/` are ported to `.github/workflows/` on GitHub-hosted `ubuntu-latest` runners, driving `scripts/release-images.sh` and `scripts/release-from-trunk.sh` without modifying those scripts or `.grain/workflows/`.

## Workflow Mapping

| Workflow File | Jobs | Grain CI Triggers | GitHub Actions Triggers | Purpose |
| :--- | :--- | :--- | :--- | :--- |
| `.github/workflows/image.yml` | `container-e2e` (`container and installer`), `guest-cache` (`sandbox guest cache`) | `workflow_dispatch` | `workflow_dispatch` (with `dry_run` input, default `true`) | On-demand full image rebuild + `tests/container` and `tests/installer` verification (`container-e2e`), and content-addressed sandbox guest cache warming (`guest-cache`). |
| `.github/workflows/release.yml` | `images` (`release images`) | `release` on `[main, candidate, "rc/v*", "release/v*"]` | `push` on `[main, candidate, "rc/v*", "release/v*"]`, `release`, `workflow_dispatch` (with `dry_run` input) | Builds `guest`, `guest-base`, `k8s-sandbox-base`, `k8s-sandbox`, and `grain` (`scripts/release-images.sh build`), gates publishing on `tests/container/...`, `tests/installer`, `tests/installer/trixie`, and `tests/installer/hostmode`, then publishes `sha-<short>`, branch (`main`, `candidate`), and release tags (`scripts/release-images.sh push`) and prunes superseded GHCR package versions (`go run ./cmd/ghcr-prune -apply=true`). |
| `.github/workflows/promote.yml` | `latest` (`promote release images`) | `release` on `[main, candidate]`, `workflow_dispatch` | `push` on `[main, candidate]`, `release`, `workflow_dispatch` (with `version` and `dry_run` inputs) | Aligns the `:latest` and `:vX.Y.Z` image tags on GHCR with the commit named by the git `latest` tag (`scripts/release-images.sh promote`). |
| `.github/workflows/release-train.yml` | `train` (`cut, soak and stage a release from trunk`) | `schedule` (`15 4 * * *`), `workflow_dispatch` | `schedule` (`15 4 * * *`), `workflow_dispatch` (with `dry_run` and `force` inputs) | Runs `scripts/release-from-trunk.sh` daily at 04:15 UTC: judges any release candidate soaking on staging (`vX.Y.0-pending` or `vX.Y.0-rc.N-discarded`) and cuts the next `vX.Y.0-rc.N` + `refs/heads/candidate` from the newest known-good `main` commit. |
| `.github/workflows/promote-release.yml` | `approve`, `promote` (`needs: [approve]`), `reject` | `workflow_dispatch` (`human: required`) | `workflow_dispatch` (with `version`, `decision: approve\|reject`, and `dry_run` inputs) | Human-only gate that promotes a pending release (`vX.Y.0-pending` -> `vX.Y.0` and `latest`) or rejects it (`vX.Y.0-rc.N-discarded`). Verifies the actor is a human (`GRAIN_CI_REQUESTED_BY_KIND=human`). |

## Image Registry and Naming Decision (`ghcr.io/bwsalmon/grain/*` via `GHCR_TOKEN` PAT)

- **Decision**: Keep publishing under **`ghcr.io/bwsalmon/grain/*`** (`RELEASE_IMAGE_REPO=bwsalmon/grain`, producing `ghcr.io/bwsalmon/grain/{grain,guest,guest-base,k8s-sandbox,k8s-sandbox-base}` with `sha-<short>`, `main`, `candidate`, `rc`, and `vX.Y.Z` / `latest` tags) and authenticate using a **`GHCR_TOKEN` Personal Access Token (PAT) secret** rather than renaming images to `ghcr.io/ai-on-gke/grain/*`.
- **Why**:
  - The deployment upgrader (`pkg/upgrade`), host installer (`scripts/setup.sh`, `scripts/install-grain.sh`), GKE deployer (`scripts/deploy-gke.sh`), sandbox guest resolver (`scripts/granule-guest-resolve.sh`), package pruner (`cmd/ghcr-prune`), and `scripts/release-images.sh` (enforced by `tests/deploy/release_images_test.go`) all expect `ghcr.io/bwsalmon/grain/*`.
  - GitHub's automatic `GITHUB_TOKEN` issued to `ai-on-gke/grain` cannot push or prune packages under the `bwsalmon` user namespace. Providing `secrets.GHCR_TOKEN` (a PAT for `bwsalmon` with `write:packages`, `read:packages`, and `delete:packages`) preserves the exact image names and tags every deployment and upgrader expects with **no image rename**.
  - If `vars.RELEASE_IMAGE_REPO` is ever overridden to `ai-on-gke/grain` in the future, the workflows automatically fall back to `secrets.GITHUB_TOKEN` and `github.actor`.
- **Optional Google Cloud Artifact Registry (`us-docker.pkg.dev`) Support**:
  - All publishing workflows include optional Workload Identity Federation (WIF) steps via `google-github-actions/auth` (`if: vars.GCP_WIF_PROVIDER != '' && vars.GCP_SA_EMAIL != ''`) followed by `gcloud auth configure-docker us-docker.pkg.dev --quiet`. When `PACKAGE_PUBLISH_REGISTRY` / `RELEASE_IMAGE_REPO` targets `us-docker.pkg.dev/<gcp-project>/grain/*`, WIF authenticates Docker without long-lived GCP keys.

## Mapping Grain CI Capabilities to GitHub Actions

1. **`package-publish`**:
   - Mapped to job-level `permissions: packages: write` (and `id-token: write` for optional WIF) plus `PACKAGE_PUBLISH_REGISTRY`, `PACKAGE_PUBLISH_USERNAME`, and `PACKAGE_PUBLISH_TOKEN` populated from `secrets.GHCR_TOKEN` and `vars.GHCR_USERNAME` (default `bwsalmon`).
   - In `release-train.yml`, the credential step also writes `/home/debian/.package-publish-{registry,username,token}` (`chmod 600`) so the pre-flight `Require the publishing credential` check fails fast before tagging any git ref if no registry token is configured.
   - In `release.yml`'s `Prune old package versions` step, `GITHUB_TOKEN` is set to `${{ secrets.GHCR_TOKEN || secrets.GITHUB_TOKEN }}` because `cmd/ghcr-prune` checks `GITHUB_TOKEN` first and requires `delete:packages` on `bwsalmon`'s GHCR packages.
2. **`image-signing` (optional capability)**:
   - Mapped to `secrets.IMAGE_SIGNING_KEY` (and optional `secrets.IMAGE_SIGNING_PASSWORD`).
   - When `secrets.IMAGE_SIGNING_KEY` is set, the workflow installs `cosign` (if not already present), writes the key to `$RUNNER_TEMP/image-signing.key` (`chmod 600`), and exports `IMAGE_SIGNING_KEY` and `IMAGE_SIGNING_PASSWORD_FILE` for `scripts/release-images.sh`.
   - When `secrets.IMAGE_SIGNING_KEY` is unset, `scripts/release-images.sh` logs `WARNING: IMAGE_SIGNING_KEY is not set; publishing ... UNSIGNED` and continues, matching `.grain/workflows`.
3. **`repo-write:ai-on-gke/grain` and `release-gate-read`**:
   - Mapped to `permissions: contents: write`, `actions/checkout` with `persist-credentials: true` and `token: ${{ secrets.REPO_WRITE_TOKEN || secrets.GITHUB_TOKEN }}`, plus `GRAIN_API_URL` (`vars.GRAIN_API_URL`) and `GRAIN_API_TOKEN` (`secrets.GRAIN_API_TOKEN`).
   - **What `scripts/release-from-trunk.sh` reads and writes**:
     - **Git repository (`origin`)**:
       - Fetches `refs/heads/main`, `refs/heads/candidate`, and all tags (`v*`, `latest`).
       - Reads `scripts/release.conf` from `origin/main` (`RELEASE_MAJOR`, `RELEASE_FLOOR_MINOR`, `RELEASE_NEVER_PROMOTE`).
       - Pushes annotated tags (`vX.Y.0-rc.N`, `vX.Y.0-rc.N-discarded`, `vX.Y.0-pending`, `vX.Y.0`, `latest`), deletes superseded `-pending` tags, and force-updates `refs/heads/candidate`.
     - **Grain API (`GRAIN_API_URL` authenticated with `Bearer $GRAIN_API_TOKEN`)**:
       - `GET /api/ci/runs?repo=ai-on-gke/grain&ref=main&limit=500`: read by `scripts/select-lkg.sh` to select the newest `main` commit where `.grain/workflows/tests.yml`, `.grain/workflows/release.yml`, and `.grain/workflows/nightly.yml` all succeeded.
       - `GET /api/tasks?limit=500`: read by `scripts/check-soak.sh` to check for open `soak-defect` / `critical-defect` tasks and completed `soak-exploration` tasks against the soaking candidate.
       - `POST /api/tasks/from-template`: called by `file_release_notes` to instantiate `.grain/templates/release-notes.yml` when staging `vX.Y.0-pending` (unless `RELEASE_NOTES_TASK=off`).
       - `POST /api/notifications/release-pending`: called by `raise_pending` to notify operators that `vX.Y.0-pending` is ready for Approve/Reject (unless `RELEASE_NOTIFY=off`).
     - **Staging deployment (`RELEASE_STAGING_URL`, default `http://127.0.0.1:4280`)**:
       - `GET /api/config` and `GET /api/system/health`: read by `scripts/check-soak.sh` to verify that staging is running the expected candidate commit, has soaked for at least 24 hours, and reports healthy status.

## Dry-Run Mode and PR Branch Protection

- Every workflow accepts a `dry_run` boolean input on `workflow_dispatch`.
- On any ref other than `main`, `candidate`, `rc/v*`, `release/v*`, or a published `release` event (for example, any PR branch), `DRY_RUN` automatically defaults to and is enforced as `true`.
- In `release.yml` and `image.yml`, when `DRY_RUN=true`, a `docker` wrapper prepended to `PATH` intercepts `docker login`, `docker push`, and `docker buildx imagetools create` while passing all local `docker build`, `docker pull`, `docker run`, and `docker inspect` invocations through to `/usr/bin/docker`. This builds all images byte-for-byte identically (including stamping the exact `sandbox-image` and `k8s-sandbox-image` `@sha256:...` digests and `vcs.revision` / `version.release` labels into the `grain` daemon image) and runs the e2e suites against the built image while pushing nothing to GHCR.

## Secrets and Variables the Operator Must Set

Configure the following in `ai-on-gke/grain` under **GitHub Repository -> Settings -> Secrets and variables -> Actions**:

| Name | Type | Where (`ai-on-gke/grain`) | Required | Value Source & Purpose |
| :--- | :--- | :--- | :--- | :--- |
| `GHCR_TOKEN` | Secret | Settings -> Secrets and variables -> Actions -> **Secrets** | **Required** (for live GHCR publish & prune) | Classic GitHub Personal Access Token (`ghp_...`) belonging to `bwsalmon` (or a package admin of `ghcr.io/bwsalmon/grain/*`) with `write:packages`, `read:packages`, and `delete:packages` scopes. Used as `PACKAGE_PUBLISH_TOKEN` by `scripts/release-images.sh` and `GITHUB_TOKEN` by `cmd/ghcr-prune`. |
| `IMAGE_SIGNING_KEY` | Secret | Settings -> Secrets and variables -> Actions -> **Secrets** | Optional | PEM-encoded Cosign private key (`cosign.key` from the deployment's `image-signing` capability store). Used by `scripts/release-images.sh` to sign published image digests; when unset, workflows log a warning and publish unsigned. |
| `IMAGE_SIGNING_PASSWORD` | Secret | Settings -> Secrets and variables -> Actions -> **Secrets** | Optional | Passphrase for `IMAGE_SIGNING_KEY` (leave empty or unset if the Cosign key is unencrypted). |
| `GRAIN_API_TOKEN` | Secret | Settings -> Secrets and variables -> Actions -> **Secrets** | **Required** for live `release-train.yml` | Bearer token from the Grain deployment's `api:release-gate-read` capability grant. Used by `scripts/release-from-trunk.sh`, `scripts/select-lkg.sh`, and `scripts/check-soak.sh` to read `/api/ci/runs` and `/api/tasks`, file the `release-notes` task, and post the `release-pending` notification. |
| `REPO_WRITE_TOKEN` | Secret | Settings -> Secrets and variables -> Actions -> **Secrets** | Optional (falls back to `GITHUB_TOKEN`) | Fine-grained PAT or GitHub App token with `contents: write` on `ai-on-gke/grain` if branch/tag protection rules block `github-actions[bot]` (`GITHUB_TOKEN`) from pushing `v*` / `latest` tags or force-updating `refs/heads/candidate` in `release-train.yml` and `promote-release.yml`. |
| `GHCR_USERNAME` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional (defaults to `bwsalmon`) | GitHub username corresponding to `GHCR_TOKEN` for `docker login ghcr.io` (`PACKAGE_PUBLISH_USERNAME`). |
| `RELEASE_IMAGE_REPO` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional (defaults to `bwsalmon/grain`) | Image repository path under the registry (`<owner>/<repo>`). Keep at default `bwsalmon/grain` so image names match `ghcr.io/bwsalmon/grain/{grain,guest,guest-base,k8s-sandbox,k8s-sandbox-base}`. |
| `PACKAGE_PUBLISH_REGISTRY` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional (defaults to `ghcr.io`) | Container registry hostname passed to `scripts/release-images.sh` (`ghcr.io` or `us-docker.pkg.dev`). |
| `GCP_WIF_PROVIDER` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional (required only if publishing to `us-docker.pkg.dev`) | Full Google Cloud Workload Identity Provider resource name (`projects/<project-number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>`) passed to `google-github-actions/auth`. |
| `GCP_SA_EMAIL` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional (required only if publishing to `us-docker.pkg.dev`) | Google Cloud Service Account email (`<sa-name>@<gcp-project>.iam.gserviceaccount.com`) with `roles/artifactregistry.writer` on the target Artifact Registry repository. |
| `GRAIN_API_URL` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | **Required** for live `release-train.yml` on hosted runners | Publicly reachable HTTPS base URL of the Grain deployment API read by `scripts/release-from-trunk.sh` (defaults to `http://127.0.0.1:4200` when unset). |
| `RELEASE_STAGING_URL` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | **Required** for live `release-train.yml` soak checks on hosted runners | Reachable base URL of the `grain-candidate` staging supervisor read by `scripts/check-soak.sh` (`/api/config`, `/api/system/health`; defaults to `http://127.0.0.1:4280` when unset). |
| `RELEASE_IMAGES` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional | Set to `off` only if `release-from-trunk.sh` should skip invoking `scripts/release-images.sh`; leave unset for normal operation. |
| `RELEASE_NOTES_TASK` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional | Set to `off` to skip filing the `release-notes` task when staging `vX.Y.0-pending`; leave unset for normal operation. |
| `RELEASE_NOTIFY` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional | Set to `off` to skip sending the `release-pending` notification; leave unset for normal operation. |

