# GitHub Actions CI (`tests.yml`, `nightly.yml`, `post-reset-e2e.yml`, `gcp-smoke.yml`, `live-agent.yml`, `live-frameworks.yml`)

This document describes the GitHub Actions ports in `.github/workflows/` of the non-agent test suites defined in `.grain/workflows/`, targeting GitHub-hosted `ubuntu-latest` runners:

- `.github/workflows/tests.yml`: per-PR and per-push suite (`.grain/workflows/tests.yml`)
- `.github/workflows/nightly.yml`: scheduled nightly and on-demand long/soak/stress suite (`.grain/workflows/nightly.yml`, plus on-demand `kvm-route-stress` for `.grain/workflows/kvm-route-stress.yml`)
- `.github/workflows/post-reset-e2e.yml`: scheduled and on-demand post-reset end-to-end qualification suite (`.grain/workflows/post-reset-e2e.yml`)
- `.github/workflows/gcp-smoke.yml`: scheduled and on-demand GCE VM and GKE cluster lifecycle smoke suite (`.grain/workflows/gcp-smoke.yml`)

## Triggers, Concurrency, and Permissions

- **Triggers (`on:`)**:
  - `tests.yml`: `push` on `main`, `pull_request`, `merge_group`, and `workflow_dispatch` (with optional comma-separated `jobs` input for selective job execution)
  - `nightly.yml`: `push` of release-candidate tags (`v*-rc.*`, excluding `v*-rc.*-*` markers such as `-discarded`), `schedule` (`cron: "11 2 * * *"`, a safety net on `main`; its `idle-gate` job skips every other job, `stage-1` included, when a `vX.Y.Z-rc.N` run concluded in the last 24h) and `workflow_dispatch` (with optional comma-separated `jobs` input, e.g. `upgrade-test` for a single-job flake retry on an RC, or `kvm-route-stress`). It is Stage 1 of RC qualification (`design/playbook/releases.playbook.md`); runs on `main` and on `refs/tags/v*` are never cancelled by a later run on the same ref, and its `stage-1` job fails unless all 14 hosted Stage 1 jobs ended `success` (a skipped job fails it). A tag pushed with the default `GITHUB_TOKEN` starts no `push` workflow, so a train pushing with it must dispatch `nightly.yml` on the tag instead
  - `post-reset-e2e.yml`: `schedule` (`cron: "23 5 * * *"`) and `workflow_dispatch`
  - `gcp-smoke.yml`: `schedule` (`cron: "41 4 * * *"`) and `workflow_dispatch`
- **Concurrency (`concurrency:`)**:
  - Every job defines its own concurrency group keyed on `${{ github.workflow }}-<job-id>-${{ github.ref }}` with `cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}`, so superseded pushes to an open PR cancel in-flight runs for that job and ref without cancelling sibling jobs in the same workflow run or `main` branch runs.
- **Timeouts (`timeout-minutes:`)**:
  - Every job sets an explicit `timeout-minutes` bound (`15` to `150` minutes depending on suite duration on 4-vCPU `ubuntu-latest` runners).
- **Permissions (`permissions:`)**:
  - Top-level `contents: read` (`actions/checkout` runs with `persist-credentials: false`).
  - `gcp-smoke.yml` additionally grants `id-token: write` so `google-github-actions/auth` can exchange a GitHub Actions OIDC token via Workload Identity Federation.
  - All third-party actions are pinned to full 40-character commit SHAs.

## Job-by-Job Mapping

### 1. Per-PR Suite (`.grain/workflows/tests.yml` -> `.github/workflows/tests.yml`)

| Job ID | Display Name | Grain CI (`.grain/workflows/tests.yml`) | GitHub Actions (`.github/workflows/tests.yml`) | Hosted Runner Status |
| :--- | :--- | :--- | :--- | :--- |
| `changes` | `changes` | Native per-job `paths:` / `paths-ignore:` cache keys evaluated by `pkg/ci` | Lightweight pre-job running `./scripts/github-ci-changes.sh` to emit per-group and per-job booleans to `$GITHUB_OUTPUT` for `pull_request` path filtering and `workflow_dispatch` `inputs.jobs` selection | Active (`ubuntu-latest`) |
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
| `terraform` | `terraform` | Runs `terraform fmt -check`, `init -backend=false`, and `validate` for `deploy/terraform/gcp` | Pins Terraform `1.9.8` via `hashicorp/setup-terraform` and runs the same format/init/validate steps | Active (`ubuntu-latest`) |
| `installer-e2e` | `installer` | Assembles `grain-installer:test` via `scripts/installer-test-image.sh` and runs `./tests/installer` (`GRAIN_INSTALLER_E2E=1`) | Same commands on `ubuntu-latest` (pulls from `docker.io/grainai/*`, authenticated when `DOCKER_PAT` is set, and uses host systemd/Docker) | Active (`ubuntu-latest`) |
| `package-cache` | `package cache` | Runs `pkg/orchestrator` real-dockerd cache tests (`GRAIN_REAL_DOCKER_CACHE_TESTS=1`) and `pkg/daemon` transparent interception test (`GRAIN_REAL_INTERCEPT_TESTS=1`) | Seeds `/etc/docker/daemon.json` with `Granulefile`'s `bip`/`default-address-pools`/`mtu`, restarts Docker, and runs the same tests | Active (`ubuntu-latest`) |
| `real-vm` | `real vm` | Formerly in `tests.yml` (now in `nightly.yml` as `kvm-sandboxes`): boots real Firecracker/Cloud-Hypervisor VMs | Resolves `grain-guest:e2e-test` (`scripts/granule-guest-resolve.sh`), configures `/dev/kvm` udev permissions, and runs the 3 PR real-VM tests on `ubuntu-latest` at 2 vCPUs / 2,048 MiB per VM (`GRAIN_REAL_VM_CPUS=2`, `GRAIN_REAL_VM_MEMORY_MB=2048`), gated on `needs.changes.outputs.real-vm` | Active (`ubuntu-latest`) |
| `real-kubernetes` | `real kubernetes` | Boots a single-node `kind` + Calico (`v3.33.0`) cluster and exercises Kubernetes `NetworkPolicy` egress isolation and real pod sandboxes (`TestK8sSandboxesAgainstARealKindCluster`) | Frees unused runner toolchains, restores cached `kindest/node:v1.36.1` + Calico/workload image archives (`~/.cache/grain-k8s` via `scripts/prepare-real-k8s-ci.sh`), trims control-plane and Calico CPU requests, and runs `TestK8sSandboxesAgainstARealKindCluster` | Active (`ubuntu-latest`) |

#### Per-Job Path Filtering (`pull_request`) and `workflow_dispatch` Job Selection

Because GitHub Actions only supports native `paths:` / `paths-ignore:` filters at the workflow trigger level (`on.pull_request`), `.github/workflows/tests.yml` runs a fast `changes` pre-job (`./scripts/github-ci-changes.sh`) and gates every downstream job with `needs: [changes]` and an `if:` condition:

- **Path filtering on `pull_request`** (mirroring `.grain/workflows/tests.yml`, plus `.github/workflows/tests.yml` itself so workflow edits run all jobs):
  - **`go_program`** (gates `go`, `go-e2e`, `ui-e2e`, `installer-e2e`): `true` when any changed file is outside `*not-read-by-the-go-program` (`docs/**` except `docs/*-surface.md`, top-level `*.md`, `LICENSE`, `NOTICE`).
  - **`go_backend`** (gates `go-orchestrator`, `go-model`, `postgres`, `run-host`, `host-script-container`, `package-cache`, `real-vm`, `real-kubernetes`): `true` when any changed file is outside `*not-read-by-the-go-backend` (`*not-read-by-the-go-program` plus `ui/**`).
  - **`go_unit`** (gates `go-unit`): `true` when any changed file is outside `*not-read-by-the-go-backend`, or touches `ui/src/state.js` or `ui/src/taskAttributes.js`.
  - **`granule`** (gates `granule`): `true` when any changed file matches `third_party/granule/**`, `scripts/run-go-tests.sh`, or `.github/workflows/tests.yml`.
  - **`frontend`** (gates `frontend`): `true` when any changed file matches `ui/**`, `Makefile`, `scripts/warm-npm-cache.sh`, or `.github/workflows/tests.yml`.
  - **`lint`** (gates `lint`): `true` when any changed file matches `scripts/**/*.sh`, `deploy/terraform/**/*.sh`, or `.github/workflows/tests.yml`.
  - **`terraform`** (gates `terraform`): `true` when any changed file matches `deploy/terraform/**` or `.github/workflows/tests.yml`.
- **Full suite on `push` and `merge_group`**: When `github.event_name` is `push` or `merge_group` (or when `git diff` against the PR base cannot be resolved cleanly), `scripts/github-ci-changes.sh` fails open (`true` for every group and job).
- **Subset selection on `workflow_dispatch` (`inputs.jobs`)**: Operators or agents can pass a comma-separated list of job IDs (e.g. `go-model,postgres`) via the `jobs` workflow input to run only those jobs on a branch; leaving `jobs` empty runs all active jobs. Grain's GitHub check reader (`pkg/forge/health.go`, `HealthFrom`) treats `"skipped"` check runs as passing (`PrClean`).

### 2. Nightly Suite (`.grain/workflows/nightly.yml` -> `.github/workflows/nightly.yml`)

Triggers: every pushed `vX.Y.Z-rc.N` tag, `cron: "11 2 * * *"`, and `workflow_dispatch` (optional `jobs`). The `stage-1` job (`stage 1 qualification verdict`) needs `idle-gate` and the 15 hosted jobs from `go-long` through `upgrade-test` below (KVM and `real-kubernetes` excluded until they are required) and is the run's Stage 1 verdict. Every job needs `idle-gate`, which outputs `run=false` only on a `schedule` run within 24h of an rc's Stage 1 (playbook §15), so that run skips cleanly instead of failing.

| Job ID | Display Name | Grain CI (`.grain/workflows/nightly.yml`) | GitHub Actions (`.github/workflows/nightly.yml`) | Hosted Runner Status |
| :--- | :--- | :--- | :--- | :--- |
| `go-long` | `go, shuffled and repeated` | Runs `make test-long` (`make frontend` + `go test -race -shuffle=on -count=2 -timeout 90m ./...`) | Sharded across `matrix.shard: [1, 2, 3, 4]` on `ubuntu-latest` with `fetch-depth: 0`, Go/Node setup, caches, `bubblewrap`, `/usr/lib/postgresql` removed, `kernel.yama.ptrace_scope=0`, standalone `/usr/bin/sleep`, structured `::error` annotations, and `go-long-log-<shard>` artifact upload on failure | Active (`ubuntu-latest`) |
| `staterepo-soak` | `state repository soak` | Runs `make soak` (2,000 rounds of `TestSoak` in `./pkg/staterepo/`) | Same commands on `ubuntu-latest` with Go setup, cache, and `git config` | Active (`ubuntu-latest`) |
| `loadtest` | `sustained load` | Runs `make loadtest` (`make frontend` + `TestLoadSustainedConcurrency` in `./tests/e2e/`) | Same commands on `ubuntu-latest` with Go/Node setup, caches, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `scalability` | `metadata and model scalability` | Runs `make scalability` (`make frontend` + `TestScalabilityLimits` in `./tests/e2e/`) | Same commands on `ubuntu-latest` with Go/Node setup, caches, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `merge-stress` | `merge liveness stress` | Runs `make merge-stress` (`TestMergeStressSustained` in `./tests/e2e/`) | Same commands on `ubuntu-latest` with Go setup, cache, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `long-term-test` | `end-to-end long term tests` | Runs `make long-term-test` (`TestLongTermEndToEndSustained` in `./tests/e2e/`) | Same commands on `ubuntu-latest` with Go setup, cache, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `long-sim` | `long randomized cluster simulation` | Runs `make long-sim` (20-minute `TestRandomizedClusterLong` in `./tests/e2e/`) | Same commands on `ubuntu-latest` with Go setup, cache, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `postgres-stress` | `postgres concurrency and stress` | Installs `postgresql` binaries and runs `GRAIN_REQUIRE_POSTGRES=1 make postgres-stress` (`./pkg/model/postgres/... -run TestPostgres`) | Declares `services: postgres` (`postgres:16-bookworm`), stops default system cluster on port 5432, ensures `postgresql` binaries are present, and runs `GRAIN_REQUIRE_POSTGRES=1 make postgres-stress` | Active (`ubuntu-latest`) |
| `tunnel-stress` | `reverse tunnel multiplexer stress` | Runs `make tunnel-stress` (30-minute `./pkg/tunnel/...` stress suite) | Same commands on `ubuntu-latest` with Go setup and cache | Active (`ubuntu-latest`) |
| `ui-e2e-long` | `ui e2e, repeated` | Runs `make test-e2e-long` (3 full passes of Chromium + WebKit Playwright e2e suite) and uploads `ui/playwright-report` + `ui/test-results` | Sharded across `matrix.pass: [1, 2, 3]` on `ubuntu-latest` (`timeout-minutes: 60` per pass) with third-party apt list cleanup, `/var/cache/playwright` caching, `::error` annotations, and `actions/upload-artifact` on failure | Active (`ubuntu-latest`) |
| `installer-variants` | `installer trixie and hostmode` | Assembles `grain-installer:test` (`GRAIN_INSTALLER_SANDBOX_IMAGE=debian:trixie`) and runs `./tests/installer/trixie` + `./tests/installer/hostmode` | Same commands on `ubuntu-latest` with `fetch-depth: 0` (required by `scripts/export-source.sh`), Go/Node setup, caches, and `git config` | Active (`ubuntu-latest`) |
| `vulns` | `govulncheck` | Runs `make vuln` (`govulncheck ./...` under `go.mod` toolchain) | Same commands on `ubuntu-latest` with Go setup and cache | Active (`ubuntu-latest`) |
| `image-vulns` | — (GitHub Actions only) | Scans the RC commit's published images with Trivy (OS and language packages) and fails on a fixable CRITICAL/HIGH finding outside `.grain/baselines/image-vulns-allowlist.yaml` ([image-vulnerability-scanning.md](image-vulnerability-scanning.md)) | `scripts/scan-images.sh --tag sha-<short>` on `ubuntu-latest` (`:main` on the schedule), Trivy DB cached | Active (`ubuntu-latest`) |
| `cmd-grain-stress` | `pkg/cli stress under load` | Runs 4-way parallel `go test -race -count=1 ./pkg/cli` rounds within a 1,800s budget | Same stress loop on `ubuntu-latest` with Go setup, cache, `bubblewrap`, and `git config` | Active (`ubuntu-latest`) |
| `kvm-sandboxes` | `real KVM sandboxes` | Resolves guest image (`scripts/granule-guest-resolve.sh`) and runs real KVM VM tests in `./pkg/orchestrator/` | Resolves `grain-guest:e2e-test` (`scripts/granule-guest-resolve.sh`), configures `/dev/kvm` udev permissions, and runs all 5 real KVM sandbox tests on `ubuntu-latest` at 2 vCPUs / 2,048 MiB per VM (`GRAIN_REAL_VM_CPUS=2`, `GRAIN_REAL_VM_MEMORY_MB=2048`) | Active (`ubuntu-latest`) |
| `kvm-route-stress` | `twenty two-VM boots` | Resolves guest image and runs 20 consecutive boots of `TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently` | Same 20-boot two-VM stress suite on `ubuntu-latest` at 2 vCPUs / 2,048 MiB per VM (`GRAIN_REAL_VM_CPUS=2`, `GRAIN_REAL_VM_MEMORY_MB=2048`); also dispatchable standalone via `nightly.yml` `workflow_dispatch` with `jobs=kvm-route-stress` | Active (`ubuntu-latest`) |
| `upgrade-test` | `upgrade from previous versions` | Builds `grain-e2e:test` (`scripts/ci-docker-build.sh`) and runs `make test-upgrade` (`./tests/container/ -run 'TestUpgradeFromPreviousVersion\|TestUpgradeFromReleasedVersions\|TestAnUpgrade'`); on GitHub Actions `GRAIN_UPGRADE_RELEASE_IMAGE=docker.io/<RELEASE_IMAGE_REPO>/grain` (with read-only fallback to legacy `ghcr.io/bwsalmon/grain/grain` for pre-cutover tags) adds real `latest -> RC` and `floor -> RC` upgrades from published releases, each of which must print `--- PASS` | Same commands on `ubuntu-latest` with `fetch-depth: 0`, Go setup, cache, and `git config` | Active (`ubuntu-latest`) |
| `real-kubernetes` | `real kubernetes` | Runs in `tests.yml` on path match; included in `nightly.yml` for continuous-release qualification | Same single-node `kind` + Calico (`v3.33.0`) setup and `TestK8sSandboxesAgainstARealKindCluster` suite on `ubuntu-latest` | Active (`ubuntu-latest`) |

### 3. Post-Reset End-to-End Qualification (`.grain/workflows/post-reset-e2e.yml` -> `.github/workflows/post-reset-e2e.yml`)

Schedule: `cron: "23 5 * * *"` and `workflow_dispatch`.

| Job ID | Display Name | Grain CI (`.grain/workflows/post-reset-e2e.yml`) | GitHub Actions (`.github/workflows/post-reset-e2e.yml`) | Hosted Runner Status |
| :--- | :--- | :--- | :--- | :--- |
| `deployments` | `deployments (SQLite and Postgres)` | Installs PostgreSQL binaries, builds `grain-core` and `grain-v1.13.0` worktree binary, and runs `./scripts/post-reset-e2e.sh` for `sqlite` and `postgres` | Same commands on `ubuntu-latest` with `fetch-depth: 0` (for tag `v1.13.0`), Go setup, cache, system `postgresql` stopped, and `git config` | Active (`ubuntu-latest`) |
| `harness` | `e2e harness (githubsim)` | Runs `go test -count=1 -timeout 25m ./tests/e2e/` | Same commands on `ubuntu-latest` with Go/Node setup, caches, `bubblewrap`, `git config`, and `make frontend` | Active (`ubuntu-latest`) |
| `security` | `route authorization` | Runs `go test -count=1 -timeout 15m ./tests/security/...` | Same commands on `ubuntu-latest` with Go setup and cache | Active (`ubuntu-latest`) |

### 4. GCP Smoke (`.grain/workflows/gcp-smoke.yml` -> `.github/workflows/gcp-smoke.yml`)

Schedule: `cron: "41 4 * * *"` and `workflow_dispatch`.

| Job ID | Display Name | Grain CI (`.grain/workflows/gcp-smoke.yml`) | GitHub Actions (`.github/workflows/gcp-smoke.yml`) | Hosted Runner Status |
| :--- | :--- | :--- | :--- | :--- |
| `smoke` | `GCE and GKE lifecycle` | Activates `/home/debian/.gcp-service-account.json` (`https://grain.dev/capabilities/gcp#gcp-key`), installs `kubectl` + `gke-gcloud-auth-plugin`, runs `scripts/gce-vm-smoke.sh --iap` and `scripts/gke-cluster-smoke.sh`, and sweeps stale resources | Gates the job at the job level (`if: vars.GCP_WIF_PROVIDER != '' \|\| vars.GCP_SA_EMAIL != ''`) so unconfigured runs conclude `skipped` rather than a false green `success` (emitting a `::warning` and failing if only one variable is set); when configured via `scripts/setup-gcp-smoke-wif.sh`, authenticates via `google-github-actions/auth` (Workload Identity Federation) and runs the GCE/GKE smoke and `if: always()` cleanup steps | Active (`ubuntu-latest`; `skipped` until WIF variables are set) |

### 5. Live Real-Model Suites (`.grain/workflows/live-agent.yml`, `live-frameworks.yml` -> `.github/workflows/live-agent.yml`, `live-frameworks.yml`)

These drive the real agent CLIs against the real model providers (`GRAIN_LIVE_AGENT_TEST=1`). They ran on grain CI until this repository moved to `ci_mode: github`, after which grain stopped firing its scheduled grain CI workflows and both suites silently stopped running (`live-frameworks` last ran 2026-10-31, `live-agent` earlier). The grain CI files are removed; the Actions files are the only source of truth.

| Job ID | Display Name | Grain CI (retired) | GitHub Actions | Hosted Runner Status |
| :--- | :--- | :--- | :--- | :--- |
| `live-agy` | `live agy run` | `capabilities: [gemini-key]`, nightly 07:37 UTC; installed agy from the `Dockerfile` pin and ran `pkg/agent/antigravity`'s live probes and `go test ./tests/e2e/ -run 'TestLive'` | Same install and tests, nightly 07:37 UTC and `workflow_dispatch`; `GEMINI_API_KEY` from environment `live-models`; skips `TestLiveClaude*` (run by `live-claude`); includes `TestLiveAgyAcceptsTheControllerToolsOfAnAPIGrant` | Active (`ubuntu-latest`; red until the secret is set) |
| `live-claude` | `live claude run` | `optional-capabilities: [claude-proxy]`, nightly 06:19 UTC; **skipped green** without a token | `go test ./pkg/agent/claude/ -run 'TestLive'` and `go test ./tests/e2e/ -run 'TestLiveClaude'` (the api-grant case), nightly 06:19 UTC and `workflow_dispatch`; `CLAUDE_CODE_OAUTH_TOKEN` from `live-models`, **required**; model `claude-haiku-4-5` unless `vars.GRAIN_LIVE_CLAUDE_MODEL` says otherwise | Active (`ubuntu-latest`; red until the secret is set) |
| `live-codex` | `live codex run` | `optional-capabilities: [openai-proxy]`; credential-free probes, then the live run when a key was present | Same; `OPENAI_API_KEY` from `live-models` is optional, and its absence is a `::warning` annotation rather than a silent skip | Active (`ubuntu-latest`) |

How the credentials are held:

- **Triggers:** `schedule` and `workflow_dispatch` only. Never `push`, `pull_request` or `pull_request_target`, so no fork or unreviewed branch runs with a key. Every job is `if: github.ref == 'refs/heads/main'`.
- **Secrets:** read only as `secrets.*` of the GitHub environment `live-models`, at job level, whose deployment branch rule admits `main` alone. Each job holds one vendor's key and never the other's. The keys are masked (`::add-mask::`) and no step prints the environment.
- **Permissions:** `contents: read` and nothing else; no `id-token`.
- **Missing secrets fail:** the first step of each job fails with `set secret X in environment live-models`. With `GRAIN_LIVE_AGENT_TEST=1` the tests' own `skipUnlessLiveRequired` fails the same way (naming the secret and the environment) rather than skipping, because a skip and a pass are the same `ok` in `go test`'s summary, and a quiet skip is how these suites went dark.
- **Spend:** each workflow's header notes a run's approximate number of model sessions. The jobs are capped at 60 (`live-agy`), 45 (`live-claude`) and 30 (`live-codex`) minutes.
- **Tests:** `tests/deploy`'s `TestOnlyDefaultBranchLiveJobsHoldAModelCredential` and `TestTheLiveFrameworkJobsKeepTheirTwoCredentialsApart` pin all of the above.

### 6. Workflows That Stay on Grain CI

These `.grain/workflows/` files have no Actions counterpart, because they need something only a grain sandbox provides:

| Workflow | Trigger | Why it stays on grain CI |
| :--- | :--- | :--- |
| `agy-surface.yml`, `claude-surface.yml`, `codex-surface.yml` | `workflow_dispatch` | They capture each agent CLI's tool and config surface inside grain's own sandbox image and network, which is what they compare against. They hold no credential. |
| `kvm-route-stress.yml` | `workflow_dispatch` | The grain-sandbox form of the stress run; the scheduled form is `nightly.yml`'s `kvm-route-stress` job. |
| `live-secrets-repo.yml` | `schedule` (05:53 UTC), `workflow_dispatch` | It needs the `https://grain.dev/capabilities/github#sandbox-repo` capability (formerly `github-sandbox`): a GitHub token with Contents: write on a private repository, which is deliberately kept out of GitHub Actions secrets. **Its schedule no longer fires in a `ci_mode: github` repository**; it needs a `.grain/schedules/` entry with `grain-ci: true` (`docs/declarations.md`) to run nightly again. |

A scheduled grain CI workflow in a `ci_mode: github` repository does not run by itself. `grain health` reports any scheduled workflow or schedule that has gone more than twice its cadence without a run, by name (`stale-schedules`).

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
   - `real-kubernetes` (`tests.yml` and `nightly.yml`) caches `~/.cache/grain-k8s` (`kind` binary, pinned Calico `v3.33.0` manifest, and `zstd`-compressed `linux/amd64` `kindest/node:v1.36.1` + workload image archives, ~691 MB total) keyed on `hashFiles('scripts/prepare-real-k8s-ci.sh')`.
4. **Bubblewrap (`bwrap`) and Ubuntu 24.04 AppArmor User Namespaces**:
   - Confinement tests in `pkg/mcp`, `pkg/daemon`, and `pkg/orchestrator` (`host_confine_test.go`, `host_sandbox_hide_test.go`) require `bwrap` to create unprivileged user namespaces when `bwrap` is installed.
   - Ubuntu 24.04 (`ubuntu-latest`) sets `kernel.apparmor_restrict_unprivileged_userns=1` by default, blocking `bwrap` from creating user namespaces. Go test jobs install `bubblewrap` alongside `expect`, `zstd`, `socat`, and `psmisc` and set `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`.
5. **PostgreSQL (`services: postgres` + Local Binaries)**:
   - `postgres` (`tests.yml`) and `postgres-stress` (`nightly.yml`) declare a `postgres:16-bookworm` service container (mapped to `5433:5432` with `pg_isready` health checks and tmpfs storage).
   - `postgres`, `postgres-stress`, `go-long`, and `post-reset-e2e`'s `deployments` stop and disable the runner's default system `postgresql` service so port `5432` remains free for `postgres.EnsureRunning` and `postgrestest` (which tune `max_connections = 200` for high-concurrency parallel store tests).
6. **`/dev/kvm` udev Rule and Nested-VM Sizing (`granule`, `real-vm`, `kvm-sandboxes`, `kvm-route-stress`)**:
   - Jobs that access `/dev/kvm` write `/etc/udev/rules.d/99-kvm4all.rules` (`KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"`), reload udev rules, and apply `chmod 0666 /dev/kvm` when `/dev/kvm` is present on the runner.
   - `real-vm` (`tests.yml`) and `kvm-sandboxes` / `kvm-route-stress` (`nightly.yml`) ensure at least 35 GB of free root disk space (reclaiming `/usr/share/dotnet`, `/usr/local/lib/android`, `/opt/ghc`, and `/opt/hostedtoolcache/CodeQL` if free space is below 35 GB), resolve the content-addressed guest image (`docker.io/grainai/sandbox:inputs-<key>`) via `scripts/granule-guest-resolve.sh`, and set `GRAIN_REAL_VM_CPUS=2` and `GRAIN_REAL_VM_MEMORY_MB=2048` so two concurrent microVMs (`2,560 MiB` cgroup limit each; ~510–580 MiB actual RSS per VM and ~2.3–2.7 GiB peak host memory used) fit comfortably without overcommit on 4-vCPU / 16 GiB `ubuntu-latest` runners (overriding `orchestrator.DefaultShape()`'s 8,192 MiB production VM default).
7. **Docker Daemon Bridge & Containerd Snapshotter Configuration (`package-cache`)**:
   - `TestPackageCachePullsThroughARealDockerd` verifies that `orchestrator.ConfigurePackageCache` preserves the sandbox's existing `/etc/docker/daemon.json` settings (`bip: "10.240.0.1/24"` and `default-address-pools: [{"base": "10.241.0.0/16", "size": 24}]` from `Granulefile`) and pulls images through `registry-mirrors` (`docker.io`) as well as `/etc/docker/certs.d/{ghcr.io,gcr.io}/hosts.toml`.
   - Stock `ubuntu-latest` runners do not include those keys in `/etc/docker/daemon.json`. The `package-cache` job seeds `/etc/docker/daemon.json` with `Granulefile`'s configuration plus `"features": {"containerd-snapshotter": true}` (so `dockerd` honors `certs.d/*/hosts.toml` mirrors) and restarts `docker.service` before running the package-cache and transparent-interception tests.
8. **Playwright & APT Third-Party Sources (`ui-e2e`, `ui-e2e-long`, `gcp-smoke`)**:
   - `ubuntu-latest` includes third-party apt lists (`/etc/apt/sources.list.d/google-chrome.list`, `/etc/apt/sources.list.d/microsoft-prod.list`) whose signing keys can cause `apt-get update` to fail. Jobs that run `apt-get update` or `make playwright-browsers` remove those unused lists first.
9. **Single-Node `kind` + Calico Trimming (`real-kubernetes`)**:
   - `TestK8sSandboxesAgainstARealKindCluster` boots a single-node `kind` cluster (`control-plane` only, `kindest/node:v1.36.1`) with Calico CNI (`v3.33.0`) to enforce `deploy/kubernetes/networkpolicy.yaml` (`grain-sandbox-network-none`: Calico Felix enforces per-pod `cali*` veth egress rules blocking internet egress such as `1.1.1.1` while permitting traffic to `app.kubernetes.io/name: grain` pods) and exercise real pod sandbox lifecycles (`Acquire`, MCP SSH tools, workspace snapshot/seed, `ConfigurePackageCacheTransparent`, `Release`). Cross-node overlay routing is not what the suite tests, so keeping a single node avoids ~1.1 GB of duplicate `containerd` image layers and ~500 MB of extra node RAM.
   - Before booting the cluster, the job removes unused preinstalled toolchains (`/usr/share/dotnet`, `/usr/local/lib/android`, `/opt/ghc`, `/opt/hostedtoolcache/CodeQL`, `/usr/local/share/boost`, `/opt/az`), `scripts/prepare-real-k8s-ci.sh` exports single-platform `linux/amd64` workload images (`quay.io/calico/{calico,node,third-party-cni-plugins}:v3.33.0`, `buildpack-deps:bookworm-curl`, `nginx:1.27-alpine`, 333 MB uncompressed) for `kind load image-archive` (avoiding `kind load docker-image` multi-arch manifest errors under Docker's `containerd-snapshotter` and freeing unpacked workload layers from host `/var/lib/docker`), and `TestK8sSandboxesAgainstARealKindCluster` trims kubeadm static-pod CPU requests (`650m` -> `175m`), `calico-node` CPU requests (`250m` -> `50m`), CoreDNS replicas (`2` -> `1`), and sandbox pod CPU requests (`CPUOvercommit: 4`, `250m` per 1-CPU sandbox).

## Jobs Formerly Gated for Self-Hosted Runners (`vars.ENABLE_SELF_HOSTED_TESTS`)

No job in `.github/workflows/` is gated on `vars.ENABLE_SELF_HOSTED_TESTS` any more. The nested-VM suites that used to be (`real-vm` in `tests.yml`, `kvm-sandboxes` and `kvm-route-stress` in `nightly.yml`, and the duplicate `kvm-route-stress.yml`, now removed) run directly on 4-vCPU / 16 GiB `ubuntu-latest` runners: the jobs configure `/dev/kvm` udev permissions (as `granule` does), resolve the guest image with `scripts/granule-guest-resolve.sh`, free runner disk when less than 35 GB is available, and size each microVM at 2 vCPUs / 2,048 MiB (`GRAIN_REAL_VM_CPUS=2`, `GRAIN_REAL_VM_MEMORY_MB=2048`) instead of the `Granulefile` defaults. The tests themselves are unchanged and prove the same thing (real VM boot, two concurrent VMs, model-proxy reachability, `network: none` isolation, host-script grants); only the guest shape is parameterised. Probe runs measured ~510–580 MiB RSS per VM and ~2.3–2.7 GiB peak host memory. `real-kubernetes` runs on `ubuntu-latest` as a single-node `kind` cluster (see item 9 above).

## Required Repository Variables and Secrets (`tests.yml`, `nightly.yml`, `post-reset-e2e.yml`, `gcp-smoke.yml`)

- **Secrets required**: **None** (`DOCKER_PAT` optional for authenticated Docker Hub pulls to avoid the 100 pulls / 6 hours per IPv4 anonymous rate limit on shared GitHub-hosted runner NAT IPs).
  - Test suites authenticate via `scripts/configure-dockerhub-auth.sh --pull-only` when `DOCKER_PAT` is present (falling back to anonymous public Docker Hub pulls on forks/PRs without secrets) or via keyless OIDC Workload Identity Federation (`gcp-smoke.yml`).
- **Repository variables (`Settings -> Secrets and variables -> Actions -> Variables`)**:
  - `GCP_WIF_PROVIDER` *(required to run `gcp-smoke.yml`, optional otherwise)*: Full resource name of the Google Cloud Workload Identity Provider (e.g. `projects/123456789/locations/global/workloadIdentityPools/github-pool/providers/github-provider`). When unset, `gcp-smoke.yml` emits a `::notice` annotation and skips cleanly.
  - `GCP_SA_EMAIL` *(required to run `gcp-smoke.yml`, optional otherwise)*: Email address of the GCP service account to impersonate for GCE/GKE lifecycle smoke tests (e.g. `grain-smoke@<project>.iam.gserviceaccount.com`). When unset, `gcp-smoke.yml` emits a `::notice` annotation and skips cleanly.
  - `GCP_PROJECT_ID` *(optional, for `gcp-smoke.yml`)*: Explicit GCP project ID to configure in `gcloud`; if omitted, `gcp-smoke.yml` derives the project ID from `GCP_SA_EMAIL`.
  - `ENABLE_SELF_HOSTED_TESTS` *(unused)*: No workflow job reads it any more; every former self-hosted job runs on `ubuntu-latest`. It can be deleted from repository variables.
  - `SELF_HOSTED_RUNNER` *(unused)*: Formerly the runner label for self-hosted jobs; no workflow job reads it any more.

- **Environment `live-models` (`Settings -> Environments`)**, for `live-agent.yml` and `live-frameworks.yml` only. Its deployment branches are limited to `main`.
  - `GEMINI_API_KEY` *(secret, required by `live-agent.yml`)*: a Gemini API key for agy.
  - `CLAUDE_CODE_OAUTH_TOKEN` *(secret, required by `live-frameworks.yml`'s `live-claude`)*: a Claude Code token (`claude setup-token`).
  - `OPENAI_API_KEY` *(secret, optional, `live-codex`)*: without it only codex's credential-free probes run, with a warning.
  - `GRAIN_LIVE_AGY_MODEL`, `GRAIN_LIVE_CLAUDE_MODEL` *(variables, optional)*: the model each live job asks for. The defaults are agy's default model and `claude-haiku-4-5`.

---

# Release and Image Publishing Workflows (`image.yml`, `release.yml`, `promote.yml`, `release-train.yml`, `promote-release.yml`, `publish-release.yml`)

The five release and image-building workflows under `.grain/workflows/` are ported to `.github/workflows/` on GitHub-hosted `ubuntu-latest` runners, driving `scripts/release-images.sh` and `scripts/release-from-trunk.sh` without modifying those scripts or `.grain/workflows/`.

## Workflow Mapping

| Workflow File | Jobs | Grain CI Triggers | GitHub Actions Triggers | Purpose |
| :--- | :--- | :--- | :--- | :--- |
| `.github/workflows/image.yml` | `container-e2e` (`container and installer`), `guest-cache` (`sandbox guest cache`) | `workflow_dispatch` | `workflow_dispatch` (with `dry_run` input, default `true`) | On-demand full image rebuild + `tests/container` and `tests/installer` verification (`container-e2e`), and content-addressed sandbox guest cache warming (`guest-cache`). |
| `.github/workflows/release.yml` | `on-demand` (`resolve on-demand commit`), `images` (`release images`) | `release` on `[main, candidate, "rc/v*", "release/v*"]` | `push` on `[main, candidate, "rc/v*", "release/v*"]`, `release`, `workflow_dispatch` (with `dry_run` and `commit` inputs; `commit` builds one superseded commit on demand, see below) | Builds `sandbox`, `sandbox-base`, `sandbox-k8s-base`, `sandbox-k8s`, and `grain` (`scripts/release-images.sh build`), gates publishing on `tests/container/...`, `tests/installer`, `tests/installer/trixie`, and `tests/installer/hostmode`, then publishes `sha-<short>`, branch (`main`, `candidate`), and release tags (`scripts/release-images.sh push`) to `docker.io/grainai/*` and prunes superseded Docker Hub tags/digests (`go run ./cmd/ghcr-prune -apply=true`). |
| `.github/workflows/promote.yml` | `on-demand` (`build the release on demand`), `latest` (`promote release images`) | `release` on `[main, candidate]`, `workflow_dispatch` | `push` on `[main, candidate]`, `release`, `workflow_dispatch` (with `version` and `dry_run` inputs) | Aligns the `:latest` and `:vX.Y.Z` image tags on Docker Hub with the commit named by the git `latest` tag (`scripts/release-images.sh promote`). On a `release` event or a dispatch (never a push), `on-demand` first builds the release's commit on demand if it has no `sha-` images (`scripts/release-images.sh on-demand`, see below). |
| `.github/workflows/release-train.yml` | `train` (`dry-run the release train`) | `workflow_dispatch` | `workflow_dispatch` (with `force`; `dry_run` is ignored) | Retired as a train: the release train is the grain task filed from `.grain/templates/release-train.yml` (playbook §0.4). This always runs `scripts/release-from-trunk.sh` dry, saying what it would cut, stage or drop, and pushes nothing. |
| `.github/workflows/publish-release.yml` | `publish` (`publish the release the train made`) | — | `push` of a `vX.Y.0` tag | Publishes what the continuous release train released (playbook §1): `scripts/release-from-trunk.sh verify vX.Y.0` (the tag names its rc, on that rc's commit, which is not dropped and still passes both qualification stages, read with `vars.RELEASE_QUALIFICATION_APP`), then `release-images.sh promote vX.Y.0` and the GitHub Release from `scripts/release-notes.sh write`. It also refuses a qualified rc the train would have staged for a person (a major bump, breaking changes, `RELEASE_NEVER_PROMOTE`, `RELEASE_AUTO_PROMOTE=false`). A tag with no `Promoted-From:` gets nothing here, neither images nor a GitHub Release: `promote-release.yml` publishes a person's approval. `contents: write` (the Release), `packages: write`, `checks: read`, `actions: write` (on-demand release dispatch). |
| `.github/workflows/rc-qualification.yml` | `housekeeping` (`re-run and cancel rc qualification jobs`) | — | `workflow_run` (`nightly` completed on an rc tag), `schedule` (`17 * * * *`, a safety wake), `workflow_dispatch` (with `dry_run`) | What the grain-task train cannot ask of GitHub Actions: reads the rc's verdict with a dry run of `scripts/release-from-trunk.sh`, then `scripts/release-train-actions.sh` dispatches the one flake retry and the infra re-runs of `nightly.yml` jobs on the rc (one run with `inputs.jobs`) and cancels the `nightly.yml` runs of an rc that is confirmed red or in `RELEASE_NEVER_PROMOTE` (§6). `actions: write`, `contents: read`: it never cuts, tags or promotes. |
| `.github/workflows/promote-release.yml` | `approve`, `promote` (`needs: [approve]`), `reject`, `rollback` | `workflow_dispatch` (`human: required`) | `workflow_dispatch` (with `version`, `decision: approve\|reject\|rollback`, `allow_breaking`, `rollback_to`, `reason`, and `dry_run` inputs) | Human-only: promotes a pending release (`vX.Y.0-pending` -> `vX.Y.0` and `latest`; `allow_breaking` is the major-version override), rejects it (`vX.Y.0-rc.N-discarded`), or rolls a bad release back (`vX.Y.Z-retracted`, and `latest` re-pointed at `rollback_to` or the newest good release). Verifies the actor is a human (`GRAIN_CI_REQUESTED_BY_KIND=human`). |

### Superseded release runs and on-demand builds

`release.yml`'s `release images` job uses `concurrency: group: release-images-${{ github.ref }}` with `cancel-in-progress: false` on `main` and `candidate`. GitHub keeps one running and **one pending** run per group, so when merges land faster than a release finishes, every pending run but the newest is cancelled. Those runs are **superseded**, not failed: the newest commit contains the others and is what the branch tags move to. Grain treats them that way -- a `cancelled` release run with a newer run of the same workflow on the same branch whose commit is a descendant files no release-fix task, raises no `pipeline.not-publishing`, and is shown as "superseded by `<sha>`". `pipeline.not-publishing` fires only for real failures, or when nothing has published on the branch for `GRAIN_RELEASE_NOT_PUBLISHING_AFTER` (a Go duration on the daemon, default `3h`) while merges keep landing.

The cost is that a superseded commit has no `docker.io/grainai/*:sha-<short>` images. If a `vX.Y.Z`, `latest` or rc tag later lands on one, `scripts/release-images.sh` (`promote`, `rc`, `pending`) builds it on demand rather than failing with "the release job never published ... for it":

- With `RELEASE_ON_DEMAND_BUILD=dispatch` (set by `promote-release.yml` and `publish-release.yml`, whose promoting jobs hold `actions: write` and pass `GH_TOKEN`, and by `promote.yml`'s `on-demand` job), it runs `gh workflow run release.yml --ref main -f commit=<sha>` and polls the registry until the commit's `sandbox`, `sandbox-k8s`, `grain` (and `registry`, where built) `sha-` images exist (`RELEASE_ON_DEMAND_POLL`, default 60s; `RELEASE_ON_DEMAND_TIMEOUT`, default 9000s), failing early if that run ends without publishing. An on-demand run already in flight for the commit is waited on instead of dispatched again. Unset, a missing image fails the promotion as before.
- `promote.yml` runs on every push to `main` and `candidate`, so its publishing job (`latest`) does not hold `actions: write`. The dispatch is a job of its own, `on-demand`, which runs only on a `release` event or a `workflow_dispatch` (`if: github.event_name != 'push'`). It holds `actions: write` and `contents: read`, reads no secrets, and runs `scripts/release-images.sh on-demand [vX.Y.Z]`. That picks the release the way `promote` does and, if its commit has no `sha-` images, builds it on demand and waits. It retags and pushes nothing. `latest` then `needs` it and runs only when it succeeded or was skipped (`push`). On a push, `on-demand` is skipped and a missing image fails `latest` as before. This split is what the security review of grain/task-3334 asked for.
- `release.yml`'s `workflow_dispatch` takes `commit: <full sha>`. Its `on-demand` job (`resolve on-demand commit`) refuses tag refs and any commit that is not on `main`, `candidate`, `rc/v*` or `release/v*`, outputs the resolved release branch (`branch`) so `release images` sets `GRAIN_CI_REF` to it rather than the dispatching ref, and skips the build when the images already exist; otherwise `release images` checks that commit out and runs the same build, e2e suites and push as a merge's release (skipping `ghcr-prune`), in its own concurrency group (`release-images-commit-<sha>`). Branch tags move only forward (`tag_may_move`, which also refuses `vX.Y.Z` and `latest` release tags), so an older commit never moves `main` back or overwrites a promoted release tag. The run is named `release <sha> (on demand)`, and grain does not count it as a release of `main`.
- By hand: `gh workflow run release.yml --ref main -f commit=<sha>`, then promote again.

## Image Registry and Naming (`docker.io/grainai/*` via `DOCKER_PAT`)

- **Registry and repositories**: Open-source images publish to **Docker Hub** under **`docker.io/grainai/*`** (`RELEASE_IMAGE_REGISTRY_DEFAULT=docker.io`, `RELEASE_IMAGE_REPO_DEFAULT=grainai` in `scripts/release-image-repos.sh`), producing `docker.io/grainai/{grain,sandbox,sandbox-base,sandbox-k8s,sandbox-k8s-base}` with `sha-<short>`, `main`, `candidate`, `rc`, `inputs-<hash>`, and `vX.Y.Z` / `latest` tags (plus Cosign `.sig` tags). `docker.io/grainai/hub` (the `grain-hub` image, also published for one release under its pre-rename name `docker.io/grainai/registry` with identical tags) is published by `scripts/release-images.sh` beside `grain`, and rebuilt with the release stamped by its `promote`, `rc` and `pending` phases.
- **Legacy GHCR compatibility**: Historical `ghcr.io/bwsalmon/grain/{grain,guest,guest-base,k8s-sandbox,k8s-sandbox-base}` images stay on GHCR untouched; existing deployments transition to `docker.io/grainai/*` via the single alias table in `pkg/installtrack/installtrack.go` and `scripts/release-image-repos.sh`.
- **Promoting a release cut before the switch** (any tag whose tree has no `scripts/release-image-repos.sh`, such as `v3.1.0`/`v3.1.1`): its release job published only to GHCR, and re-running it would publish to GHCR again. `scripts/release-images.sh promote` therefore copies whatever `docker.io/grainai/*` lacks -- `{sandbox,sandbox-k8s,sandbox-base,sandbox-k8s-base}:sha-<short>` from `ghcr.io/bwsalmon/grain/{guest,k8s-sandbox,guest-base,k8s-sandbox-base}:sha-<short>`, and GHCR's already-stamped `grain:vX.Y.Z` (or `grain:sha-<short>` when there is none, checking `org.opencontainers.image.revision` on the resolved digest before copying) -- registry-side by digest (`docker buildx imagetools create`), refuses any copy whose digest differs from its source, copies the stamped grain image only once both sandbox images are on Docker Hub (so a refused promotion leaves no `vX.Y.Z` behind), signs a copy or a rebuilt `grain:vX.Y.Z` with the job's key only when its source digest already verifies against that same key (otherwise it is left unsigned with a warning, or refused under `REQUIRE_IMAGE_SIGNING`), and then promotes as usual, so Docker Hub `vX.Y.Z` and `latest` carry the exact digests that were tested on GHCR. If GHCR lacks them too, promotion says so instead of the generic "re-run that commit's release" advice.
- **Docker Hub limits and tag immutability**:
  - **Rate limits and OAT scopes**: Unauthenticated Docker Hub pulls allow 100 pulls / 6 hours per IPv4 address. GitHub Actions test workflows call `./scripts/configure-dockerhub-auth.sh --pull-only` so runners authenticate pulls whenever `DOCKER_PAT` is available and includes **Public Repo Read-Only** scope (probed via `library/debian:pull` on `auth.docker.io`; repository-scoped Organization Access Tokens without Public Repo Read-Only leave `~/.docker/config.json` unauthenticated). In publish mode (`./scripts/configure-dockerhub-auth.sh`), Docker Hub credentials are validated in a temporary `DOCKER_CONFIG` and `~/.docker/config.json` is never written, while `scripts/release-images.sh` scopes its publishing login to an isolated `DOCKER_CONFIG` so no Docker Hub credential is active when `docker build` pulls upstream `library/*` base images.
  - **Tag immutability**: If Docker Hub tag immutability is enabled on `grainai/*`, configure the pattern strictly as `^v[0-9]+\.[0-9]+\.[0-9]+$`. Never match moving tags (`latest`, `main`, `candidate`, `rc`, branch tags) or `sha-*` (`scripts/release-images.sh promote` and `rc` re-point `grain:sha-<short>` onto the release-stamped image).
- **Optional Google Cloud Artifact Registry (`us-docker.pkg.dev`) Support**:
  - All publishing workflows include optional Workload Identity Federation (WIF) steps via `google-github-actions/auth` (`if: vars.GCP_WIF_PROVIDER != '' && vars.GCP_SA_EMAIL != ''`) followed by `gcloud auth configure-docker us-docker.pkg.dev --quiet`. When `PACKAGE_PUBLISH_REGISTRY` / `RELEASE_IMAGE_REPO` targets `us-docker.pkg.dev/<gcp-project>/grain/*`, WIF authenticates Docker without long-lived GCP keys.

## Mapping Grain CI Capabilities to GitHub Actions

On grain CI these are methods of the in-process release server (`https://grain.dev/capabilities/release#package-publish`, `#image-signing`, `#registry-push`; the bare names are one-release aliases, see [ci-capabilities.md](ci-capabilities.md)). The names below are those grain CI capabilities; GitHub Actions has no capability grants and maps each to secrets and permissions instead.

1. **`release#package-publish`**:
   - Mapped via `scripts/configure-dockerhub-auth.sh` to `PACKAGE_PUBLISH_REGISTRY` (default `docker.io`), `PACKAGE_PUBLISH_USERNAME` (from `vars.DOCKERHUB_USERNAME`, default `grainai`), and `PACKAGE_PUBLISH_TOKEN` (from `secrets.DOCKER_PAT`). Missing `DOCKER_PAT` or a failed `docker login` fails fast before any build or push step runs.
   - In `release-train.yml`, `scripts/configure-dockerhub-auth.sh --write-files` also writes `/home/debian/.package-publish-{registry,username,token}` (`chmod 600`) so the pre-flight `Require the publishing credential` check fails fast before tagging any git ref if no registry token is configured.
   - In `release.yml`'s `Prune old package versions` step, `cmd/ghcr-prune` authenticates against the Docker Hub v2 API with `DOCKER_PAT` and `DOCKERHUB_USERNAME` (default `grainai`), while retaining `-registry=ghcr.io` compatibility when overridden.
2. **`release#image-signing` (optional capability)**:
   - Mapped to `secrets.IMAGE_SIGNING_KEY` (and optional `secrets.IMAGE_SIGNING_PASSWORD`).
   - When `secrets.IMAGE_SIGNING_KEY` is set, the workflow installs `cosign` (if not already present), writes the key to `$RUNNER_TEMP/image-signing.key` (`chmod 600`), and exports `IMAGE_SIGNING_KEY` and `IMAGE_SIGNING_PASSWORD_FILE` for `scripts/release-images.sh`.
   - When `secrets.IMAGE_SIGNING_KEY` is unset, `scripts/release-images.sh` logs `WARNING: IMAGE_SIGNING_KEY is not set; publishing ... UNSIGNED` and continues, matching `.grain/workflows`.
3. **`repo#write` `{repo: ai-on-gke/grain}` and `api#release-gate-read`** (formerly `repo-write:ai-on-gke/grain` and `release-gate-read`):
   - In `release-train.yml` (always dry since the grain-task train took over) mapped to `permissions: contents: write`, `actions/checkout` with `persist-credentials: true` and `token: ${{ secrets.REPO_WRITE_TOKEN || secrets.GITHUB_TOKEN }}`, plus the optional `GRAIN_API_URL` (`vars.GRAIN_API_URL`) and `GRAIN_API_TOKEN` (`secrets.GRAIN_API_TOKEN`). The live train is the grain task from `.grain/templates/release-train.yml`, which pushes with its template's `push:` grant.
   - **What `scripts/release-from-trunk.sh` reads and writes**:
     - **Git repository (`origin`)**:
       - Fetches `refs/heads/main`, `refs/heads/candidate`, and all tags (`v*`, `latest`).
       - Reads `scripts/release.conf` from `origin/main` (`RELEASE_MAJOR`, `RELEASE_FLOOR_MINOR`, `RELEASE_NEVER_PROMOTE`).
       - The train pushes annotated tags `vX.Y.0-rc.N`, `vX.Y.0-rc.N-discarded` and `vX.Y.0-pending`, force-updates `refs/heads/candidate`, and makes `release/vX.Y.0` for an rc.3. Only `promote-release.yml` (a person's approve, reject or rollback) pushes `vX.Y.0`, `vX.Y.Z-retracted` or `latest` and deletes a `-pending` tag.
     - **Grain API (`GRAIN_API_URL` authenticated with `Bearer $GRAIN_API_TOKEN`)**:
       - `GET /api/ci/runs?repo=ai-on-gke/grain&ref=main&limit=500`: read only when `RELEASE_CI_SOURCE=grain` (a repository gated by grain CI). This repository's gate is GitHub Actions (`ci_mode: github`), so grain CI has no `tests`/`release`/`nightly` verdicts for `main`, and `release-train.yml` sets `RELEASE_CI_SOURCE=github` instead (below).
       - `POST /api/tasks/from-template`: called by `file_release_notes` to instantiate `.grain/templates/release-notes.yml` when staging `vX.Y.0-pending` (unless `RELEASE_NOTES_TASK=off`).
       - `POST /api/notifications/release-pending`: called by `raise_pending` to notify operators that `vX.Y.0-pending` is ready for Approve/Reject (unless `RELEASE_NOTIFY=off`).
     - **GitHub REST API (`GITHUB_TOKEN`, `permissions: actions: write`, `checks: read`)**:
       - `GET /repos/ai-on-gke/grain/actions/workflows/{tests,release}.yml/runs?branch=main` (up to `RELEASE_GITHUB_PAGES`, default 3, pages of 100): read by `scripts/release-from-trunk.sh` and judged by `scripts/candidate-gate.sh select-lkg` to select the newest `main` commit where `.github/workflows/tests.yml` and `release.yml` succeeded (`nightly.yml` is Stage 1, run on the rc after it is cut). Only `push`, `schedule` and `workflow_dispatch` runs of the repository itself count; a pull request's or a fork's run never vouches for a commit. Each workflow run is one verdict (re-running failed jobs updates the run in place); in progress is pending, `cancelled`/`timed_out`/`startup_failure` are infra.
       - `GET .../actions/workflows/nightly.yml/runs?head_sha=<rc_sha>` and each run's `jobs?filter=all`, and `GET .../commits/<rc_sha>/check-runs?check_name=grain/release-qualification`: read for `scripts/candidate-gate.sh check-qualification` (Stage 1 and Stage 2).
       - `POST .../actions/workflows/nightly.yml/dispatches` (on the rc tag, with `inputs.jobs` for a retry) and `POST .../actions/runs/<id>/cancel`: made by `scripts/release-train-actions.sh` in `rc-qualification.yml` (`actions: write`).
     - There is no staging deployment and no soak: `RELEASE_STAGING_URL` and `scripts/check-soak.sh` are retired.

## Dry-Run Mode and PR Branch Protection

- Every workflow accepts a `dry_run` boolean input on `workflow_dispatch`.
- On any ref other than `main`, `candidate`, `rc/v*`, `release/v*`, or a published `release` event (for example, any PR branch), `DRY_RUN` automatically defaults to and is enforced as `true`.
- In `release.yml` and `image.yml`, when `DRY_RUN=true`, a `docker` wrapper prepended to `PATH` intercepts `docker login`, `docker push`, and `docker buildx imagetools create` while passing all local `docker build`, `docker pull`, `docker run`, and `docker inspect` invocations through to `/usr/bin/docker`. This builds all images byte-for-byte identically (including stamping the exact `sandbox-image` and `k8s-sandbox-image` `@sha256:...` digests and `vcs.revision` / `version.release` labels into the `grain` daemon image) and runs the e2e suites against the built image while pushing nothing to GHCR.

## Secrets and Variables the Operator Must Set

Configure the following in `ai-on-gke/grain` under **GitHub Repository -> Settings -> Secrets and variables -> Actions**:

| Name | Type | Where (`ai-on-gke/grain`) | Required | Value Source & Purpose |
| :--- | :--- | :--- | :--- | :--- |
| `DOCKER_PAT` | Secret | Settings -> Secrets and variables -> Actions -> **Secrets** | **Required** (for live Docker Hub publish & prune; optional for authenticated CI pulls) | Docker Hub Personal Access Token or Organization Access Token with Read, Write, Delete access to `docker.io/grainai/*`. Used as `PACKAGE_PUBLISH_TOKEN` by `scripts/release-images.sh` and by `cmd/ghcr-prune` against the Docker Hub v2 API. |
| `IMAGE_SIGNING_KEY` | Secret | Settings -> Secrets and variables -> Actions -> **Secrets** | Optional | PEM-encoded Cosign private key (`cosign.key` from the deployment's `image-signing` capability store). Used by `scripts/release-images.sh` to sign published image digests; when unset, workflows log a warning and publish unsigned. |
| `IMAGE_SIGNING_PASSWORD` | Secret | Settings -> Secrets and variables -> Actions -> **Secrets** | Optional | Passphrase for `IMAGE_SIGNING_KEY` (leave empty or unset if the Cosign key is unencrypted). |
| `REPO_WRITE_TOKEN` | Secret | Settings -> Secrets and variables -> Actions -> **Secrets** | **Required** for live `promote-release.yml` (approve, reject and rollback) where tag rulesets refuse `github-actions[bot]` | Fine-grained token with `contents: write` on `ai-on-gke/grain` only, for the `vX.Y.0`, `latest` and `-retracted` tags a person's approval or rollback pushes. |
| `GRAIN_API_TOKEN` | Secret | Settings -> Secrets and variables -> Actions -> **Secrets** | Optional | Bearer token from the Grain deployment's `api:release-gate-read` capability grant, for a deployment a hosted runner can reach. Used by `scripts/release-from-trunk.sh` to read `/api/ci/runs` (`RELEASE_CI_SOURCE=grain` only), file the `release-notes` task, and post the `release-pending` notification; without it the deployment notices the `-pending` tag itself. |
| `DOCKERHUB_USERNAME` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional (defaults to `grainai`) | Docker Hub username corresponding to `DOCKER_PAT` for `docker login docker.io` and `cmd/ghcr-prune`. |
| `RELEASE_IMAGE_REPO` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional (defaults to `grainai`) | Image repository namespace under the registry. Keep at default `grainai` so image names match `docker.io/grainai/{grain,sandbox,sandbox-base,sandbox-k8s,sandbox-k8s-base}`. |
| `PACKAGE_PUBLISH_REGISTRY` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional (defaults to `docker.io`) | Container registry hostname passed to `scripts/release-images.sh` (`docker.io`, `ghcr.io`, or `us-docker.pkg.dev`). |
| `GCP_WIF_PROVIDER` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | **Required** for `gcp-smoke.yml` (optional for `us-docker.pkg.dev` image publishing) | Full Google Cloud Workload Identity Provider resource name (`projects/<project-number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>`) printed by `scripts/setup-gcp-smoke-wif.sh` and passed to `google-github-actions/auth`. |
| `GCP_SA_EMAIL` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | **Required** for `gcp-smoke.yml` (optional for `us-docker.pkg.dev` image publishing) | Google Cloud Service Account email (`grain-gcp-smoke@<gcp-project>.iam.gserviceaccount.com`) printed by `scripts/setup-gcp-smoke-wif.sh` (plus `roles/artifactregistry.writer` if also publishing images to `us-docker.pkg.dev`). |
| `GCP_PROJECT_ID` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional (inferred from `GCP_SA_EMAIL` when matching `*@<project>.iam.gserviceaccount.com`) | Explicit GCP project ID for `gcp-smoke.yml`; required only when `GCP_SA_EMAIL` does not embed the target smoke project's ID. |
| `GRAIN_API_URL` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional | Base URL of the Grain deployment API, if a GitHub-hosted runner can reach it, for the release-notes task and the `release-pending` notification (with `GRAIN_API_TOKEN`). A live train no longer needs it: verdicts come from GitHub. |
| `RELEASE_CI_SOURCE` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional (defaults to `github`) | Where `release-train.yml` reads `main`'s required verdicts: `github` (this repository's GitHub Actions workflow runs) or `grain` (`GET /api/ci/runs` on the Grain deployment, for a repository still gated by grain CI). |
| `RELEASE_IMAGES` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional | Set to `off` only if `release-from-trunk.sh` should skip invoking `scripts/release-images.sh`; leave unset for normal operation. |
| `RELEASE_NOTES_TASK` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional | Set to `off` to skip filing the `release-notes` task when staging `vX.Y.0-pending`; leave unset for normal operation. |
| `RELEASE_NOTIFY` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | Optional | Set to `off` to skip sending the `release-pending` notification; leave unset for normal operation. |
| `RELEASE_QUALIFICATION_APP` | Variable | Settings -> Secrets and variables -> Actions -> **Variables** | **Required** for an rc to pass Stage 2, unless `scripts/release.conf` sets it | Slug or numeric id of the GitHub App grain's forge connection posts the `grain/release-qualification` and `grain/rc-hold` check runs as. `scripts/candidate-gate.sh` judges only that app's check run and reports a same-named one from any other app (an Actions workflow with `checks: write`, another installed app) as `IGNORED`. Empty here, `scripts/release-from-trunk.sh` reads `RELEASE_QUALIFICATION_APP` from `scripts/release.conf` as `main` has it; unset in both, it trusts no check run and the train stops at Stage 2 rather than guess. Set explicitly to `none` (for a forge connection that is a personal access token, which cannot post check runs), it trusts no check run and skips Stage 2, so Stage 1 (`nightly.yml`) alone qualifies an rc, and no `grain/rc-hold` holds the next one. It is never `none` by default. A numeric id also narrows the GitHub query (`app_id=`). |

### Setting Up Workload Identity Federation for `gcp-smoke.yml`

`.github/workflows/gcp-smoke.yml` is scheduled-only (`cron: "41 4 * * *"` and `workflow_dispatch`, never `pull_request` or `push`), so required PR checks are unaffected when it skips. While both `GCP_WIF_PROVIDER` and `GCP_SA_EMAIL` are unset, the `smoke` job's job-level `if:` skips the job (and therefore the workflow run) with conclusion `skipped` rather than reporting a false green `success`; if only one of the two variables is set, the first step emits a `::warning` annotation and fails so an incomplete configuration is caught immediately.

To provision Workload Identity Federation idempotently in a GCP project of your choosing (no long-lived service account keys):

```sh
./scripts/setup-gcp-smoke-wif.sh <gcp-project-id>
```

Re-running `./scripts/setup-gcp-smoke-wif.sh <gcp-project-id>` is safe and converges existing resources in place (pass `--dry-run` first to inspect the exact `gcloud` commands without mutating GCP). Specifically, the script:

1. **Enables required APIs**: `iam.googleapis.com`, `iamcredentials.googleapis.com`, `sts.googleapis.com`, `compute.googleapis.com`, `container.googleapis.com`, plus `iap.googleapis.com`, `oslogin.googleapis.com`, and `cloudresourcemanager.googleapis.com`.
2. **Creates Workload Identity Pool and OIDC Provider**:
   - Pool `grain-gcp-smoke` (separate from `bootstrap-gcp.sh`'s `grain` pool so running in a shared project never overwrites another workflow's provider) and OIDC provider `github` (`https://token.actions.githubusercontent.com`).
   - **Attribute condition (`assertion.repository == 'ai-on-gke/grain'` plus a ref allowlist)**: restricts token exchange to workflows running in `ai-on-gke/grain` on `refs/heads/main`, `refs/heads/candidate`, `refs/heads/rc/v*`, `refs/heads/release/v*` or `refs/tags/v*`. Those are the refs `gcp-smoke.yml` may be dispatched on and where `.github/workflows/{release,promote,promote-release,release-train,image}.yml` (which also authenticate with `vars.GCP_WIF_PROVIDER` and `vars.GCP_SA_EMAIL` when set) run. Trusting the repository alone is not enough: a `pull_request` run (`refs/pull/N/merge`) or a push to any other branch, such as an agent's task branch, runs that branch's own unreviewed workflow files, which can request `id-token: write` and would otherwise obtain this service account before review. Operators who keep those variables to `main`-only runs can pass `--ref refs/heads/main` to tighten the condition to exactly `assertion.repository == 'ai-on-gke/grain' && assertion.ref == 'refs/heads/main'`.
3. **Creates `grain-gcp-smoke@<project>.iam.gserviceaccount.com` with least-privilege roles**:
   - Project-level `roles/compute.instanceAdmin.v1`: create, tag, set SSH metadata on, list, and delete the `e2-micro` VM and its auto-delete boot disk (`scripts/gce-vm-smoke.sh`), and list VPC subnets (`scripts/gke-cluster-smoke.sh`).
   - Project-level `roles/compute.osLogin`: SSH into the smoke VM when OS Login (`enable-oslogin=TRUE`) is enforced on the project.
   - Project-level `roles/iap.tunnelResourceAccessor`: reach TCP port 22 on the `--no-address` smoke VM via `gcloud compute ssh --tunnel-through-iap` (`scripts/gce-vm-smoke.sh --iap`).
   - Project-level `roles/container.admin`: create, resize, relabel, enable autoscaling on, list, and delete the zonal GKE cluster and node pool, plus run the `kubectl` node/deployment/pod checks (`scripts/gke-cluster-smoke.sh`; `container.clusterAdmin` alone lacks the Kubernetes RBAC verbs for `kubectl create/scale deployment`).
   - Service-account-level `roles/iam.serviceAccountUser` **on `grain-gcp-smoke@...` itself** (not project-wide): lets GKE attach the smoke SA as its own node-pool identity (`SERVICE_ACCOUNT_SHAPE=self`) without granting `actAs` on the project's default Compute Engine service account or any other SA (`gce-vm-smoke.sh` passes `--no-service-account --no-scopes` by default; pass `--allow-default-compute-sa` only if exercising `SERVICE_ACCOUNT_SHAPE=default`).
   - Service-account-level `roles/iam.workloadIdentityUser` on `grain-gcp-smoke@...` for `principalSet://iam.googleapis.com/projects/<project-number>/locations/global/workloadIdentityPools/grain-gcp-smoke/attribute.repository/ai-on-gke/grain`.
4. **Ensures VPC prerequisites on `default` in `us-central1`** (skip with `--skip-network-prereqs`):
   - An ingress firewall rule (`gcp-smoke-allow-iap-ssh`, if `default-allow-ssh` or an equivalent untagged IAP rule is absent) admitting `35.235.240.0/20` on `tcp:22` for `gce-vm-smoke.sh --iap`.
   - A Cloud Router and Cloud NAT (`gcp-smoke-router` / `gcp-smoke-nat`, if no NAT gateway already covers `default` in `us-central1`) so `gke-cluster-smoke.sh`'s `--enable-private-nodes` nodes can pull `nginx:alpine` from Docker Hub.
5. **Prints the repository Variables (not Secrets)** to set under **Settings -> Secrets and variables -> Actions -> Variables**:
   - `GCP_WIF_PROVIDER`
   - `GCP_SA_EMAIL`
   - `GCP_PROJECT_ID` (optional when `GCP_SA_EMAIL` ends in `@<project>.iam.gserviceaccount.com`)
6. **Cost and cleanup**:
   - **Per-run cost**: ~$0.01–$0.04 per ~14-minute run (`e2-micro` + 10 GB disk for ~90s is <$0.001; a 1->2 node `e2-medium` zonal GKE cluster with 50 GB `pd-balanced` disks for ~12m is ~$0.012 in compute/disk, plus $0 GKE cluster fee under the billing account's single free zonal cluster tier or ~$0.025 without it). Running once daily costs ~$0.40–$1.20/month (plus ~$1.00/month if `gcp-smoke-nat` was created in a project with no existing Cloud NAT). WIF pools, providers, service accounts, and firewall rules cost $0.
   - **Cleanup on failure**: both `scripts/gce-vm-smoke.sh` and `scripts/gke-cluster-smoke.sh` register `trap cleanup EXIT` to delete `$VM` (and verify its auto-delete boot disk is gone) and `$CLUSTER` (including node VMs and disks) on normal exit, `set -e` errors, or interrupts. As a second layer, `.github/workflows/gcp-smoke.yml` runs `Delete anything left behind` under `if: always() && steps.auth.outcome == 'success'`, sweeping both any `gce-smoke-*` VM or `gke-smoke-*` cluster created by the current workflow run and any stale `labels.purpose=gce-vm-smoke` / `resourceLabels.purpose=gke-cluster-smoke` resources older than 2 hours.

# Reading Actions runs from an agent

This repository is gated by GitHub Actions (`ci_mode: github`), so its
CI lives on GitHub rather than in grain CI. An agent still reads it
through grain, with the `ci:read` routes and the sandbox `grain` CLI, and
needs no GitHub credential of its own. The daemon fetches through its
own forge connection: the GitHub App or token for github.com, and the
host's own tool (`ForgeViaCommand`) for a GitHub Enterprise host. The
full reference is [the agent API's "GitHub Actions runs"](agent-api.md#github-actions-runs).

To check whether the latest `release.yml` on `main` passed, and read the
failing job's log if it did not:

```sh
grain ci list --repo ai-on-gke/grain --ref main --provider github-actions --workflow release.yml
# gha:ai-on-gke:grain:123 github-actions/release.yml main 1a2b3c4d5e6f failure 2026-11-01T10:00:00Z failing: publish
grain ci show gha:ai-on-gke:grain:123
grain ci log gha:ai-on-gke:grain:123 --job publish --lines 80
```

The same reads over the API:

- `GET /api/ci/runs?repo=ai-on-gke/grain&ref=main&provider=github-actions&workflow=release.yml`
- `GET /api/ci/runs/gha:ai-on-gke:grain:123`
- `GET /api/ci/runs/gha:ai-on-gke:grain:123/log?job=publish&tail=80`

An Actions run's id is `gha:<owner>:<name>:<run id>`. For a GHE
repository the host comes first: `gha:ghe.example.com:experimental:grain:<run id>`.

How the reads behave:

- When the forge cannot be asked, the list is built from grain's own
  polled record of the repository's Actions jobs. That happens when
  there is no forge connection, when the hour's API budget is low, or
  when the forge read fails, and the answer's `actionsNote` says which.
- Artifacts are listed, not downloaded.
- Only the repositories this deployment works in (its target repos, or
  every repository while that list is empty) are read, whatever a
  caller's grant covers: the daemon's credential may reach others, and
  a `gha:` id names whichever repository its caller wrote.
- `grain ci wait` on a task's branch waits on the GitHub checks that
  gate it.
