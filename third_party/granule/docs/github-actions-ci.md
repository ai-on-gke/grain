# GitHub Actions CI

This repository's CI is GitHub Actions: the workflows in
`.github/workflows/`, on GitHub-hosted `ubuntu-latest` runners. The
deployment's per-repo setting (`ci_mode: github`, in grain/config) makes
the Actions checks the merge gate. They replaced grain CI's
`.grain/workflows/`, which has been deleted; a workflow change belongs in
`.github/workflows/` only.

Grain still reviews the scheduled workflows. Each one has a declaration in
`.grain/schedules/` that files a review task when a run of it on `main`
ends in `failure`, `timed_out` or `startup_failure` (see "Review of failed
scheduled runs" below).

## Operator checklist

1. **Actions enabled** for `gke-labs/granule` (Settings → Actions →
   General), with workflow permissions left at "Read repository contents".
   Every workflow asks for anything more per job.
2. **Required status checks** (Settings → Branches, or a ruleset on
   `main`). Require these check names from `ci.yml`:
   - `changes`
   - `test`
   - `published guest rootfs`
   - `rootless VM container`
   - `sandbox access VM`

   A job its path filter skips counts as passing, so a docs-only change is
   not held up by a VM boot.
3. **GHCR read access, then `ENABLE_GUEST_IMAGE_TESTS`.** `rootless VM
   container` and `sandbox access VM` pull
   `ghcr.io/gke-labs/granule/alpine:latest` with the job's `GITHUB_TOKEN`
   (`packages: read`). Today GHCR refuses that pull: the package doesn't
   grant this repository access. (Grain CI reaches it through grain's own
   authenticated GHCR mirror.) So both jobs are **skipped**, with a
   notice from the `changes` job, until you:
   - open the `alpine` package (and `debian`, `debian12` if tests start
     using them) under the gke-labs organisation's Packages, then Package
     settings → Manage Actions access, and add `gke-labs/granule` with
     **Read**;
   - set the repository variable `ENABLE_GUEST_IMAGE_TESTS` to `true`.

   Until then those two checks pass as skipped, so requiring them is
   harmless. Once the variable is set they boot real guests.
4. **GHCR write access for releases.** `release.yml` pushes the guest
   images with `GITHUB_TOKEN` (`packages: write`). Each
   `ghcr.io/gke-labs/granule/*` package must grant this repository
   **Write** under "Manage Actions access". If that cannot be arranged,
   set the `GHCR_TOKEN` secret and the `GHCR_USERNAME` variable instead
   (see the table below).
5. **GCP, for `gce-build.yml`, `storage-benchmark.yml`,
   `disk-direct-benchmark.yml` and `e2e-ipv6-gke.yml`.** These authenticate keylessly through Workload
   Identity Federation. Until both variables are set, their real job is
   **skipped** (not passed), and a `GCP credentials` job posts a notice
   saying so. To enable them:
   - create a Workload Identity Pool and an OIDC provider for
     `https://token.actions.githubusercontent.com`, with an attribute
     condition of `assertion.repository == 'gke-labs/granule'`;
   - grant the pool's principal for this repository
     `roles/iam.workloadIdentityUser` on a service account with the roles
     the old `gcp-key` service account had (Compute Instance Admin and
     Service Account User for the GCE VMs; Kubernetes Engine Admin and
     Compute Network Admin for the GKE run);
   - set `GCP_WIF_PROVIDER`, `GCP_SA_EMAIL` and `GCP_PROJECT_ID`.
6. **Self-hosted runner image builds** (`github-runner-images.yml`).
   These run only when `ENABLE_SELF_HOSTED_TESTS` is `true` and a runner
   carrying the labels in `SELF_HOSTED_RUNNER` (default `self-hosted,
   linux, x64, kontur-image-builder`) is registered. They also need the
   `AZURE_*` variables the workflow already reads.
7. **Then flip the CI setting** in grain/config (`ci_mode: github` for
   `gke-labs/granule`; done), and drop the `gcp#gcp-key` and
   `package-publish` entries from the repository's `ci_capabilities`
   there. With `.grain/workflows/` gone, no grain CI job uses them.

## Secrets and variables

Set these under Settings → Secrets and variables → Actions. None is needed
for the per-PR checks to pass: everything in `ci.yml` uses only the job's
own `GITHUB_TOKEN`.

| Name | Type | Required | Used by | What it is |
| :--- | :--- | :--- | :--- | :--- |
| `GCP_WIF_PROVIDER` | Variable | For the GCP workflows | `gce-build.yml`, `storage-benchmark.yml`, `disk-direct-benchmark.yml`, `e2e-ipv6-gke.yml` | Full provider resource name: `projects/<number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>`. |
| `GCP_SA_EMAIL` | Variable | For the GCP workflows | same | The service account the provider impersonates. It replaces the `gcp#gcp-key` key file. |
| `GCP_PROJECT_ID` | Variable | Recommended | same | The project the VMs and cluster are created in (`GCE_PROJECT` / `GKE_PROJECT`). Without it, `test/gce-build.sh`, `gce-bench.sh` and `test/e2e-ipv6.sh` use gcloud's configured project. |
| `GHCR_TOKEN` | Secret | Optional | `release.yml` | A token with `write:packages`, used instead of `GITHUB_TOKEN` when the packages cannot grant this repository write access. |
| `GHCR_USERNAME` | Variable | With `GHCR_TOKEN` | `release.yml` | The account `GHCR_TOKEN` belongs to. |
| `ENABLE_GUEST_IMAGE_TESTS` | Variable | Once GHCR access is granted | `ci.yml` (`rootless-vm`, `sandbox-access-vm`) | `true` once `ghcr.io/gke-labs/granule/alpine` grants this repository read access. Until then both jobs are skipped with a notice. |
| `ENABLE_SELF_HOSTED_TESTS` | Variable | Optional | `github-runner-images.yml` | `true` once a self-hosted image builder is registered. |
| `SELF_HOSTED_RUNNER` | Variable | Optional | `github-runner-images.yml` | JSON array of runner labels, for example `["self-hosted","linux","x64","kontur-image-builder"]`. |
| `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`, `AZURE_RUNNER_IMAGE_RESOURCE_GROUP`, `AZURE_RUNNER_IMAGE_LOCATION` | Variables | With the self-hosted builder | `github-runner-images.yml` | Unchanged from before the move to grain CI. |

No credential is stored in the repository. `GITHUB_TOKEN` is the
per-job token GitHub mints, and GCP access is a short-lived token
exchanged over OIDC.

## Capability mapping

| Grain CI capability | GitHub Actions |
| :--- | :--- |
| `https://grain.dev/capabilities/gcp#gcp-key` (a key file at `/home/debian/.gcp-service-account.json`) | `permissions: id-token: write` on the job, then `google-github-actions/auth` with `vars.GCP_WIF_PROVIDER` / `vars.GCP_SA_EMAIL`. The jobs set `GCE_KEY_FILE` to a path that does not exist. The scripts otherwise activate whatever `GOOGLE_APPLICATION_CREDENTIALS` names as a service-account key, and the WIF credential is not one. With no key file they use gcloud's active login, which the auth step configured. |
| `package-publish` (token and username files) | `permissions: packages: write` on `release.yml`'s job, passing `GITHUB_TOKEN` (or the optional `GHCR_TOKEN` secret) to `deploy/publish-images.sh` as `GHCR_TOKEN`. The registry is GHCR, not Artifact Registry, so no WIF is involved. |
| A sandbox with `/dev/kvm` (no `runs-on`) | `ubuntu-latest`, which has `/dev/kvm`, plus a udev rule. |
| grain's proxy, CA and registry mirror | Nothing: a hosted runner reaches github.com, ghcr.io and Docker Hub directly. `GITHUB_PROXY` is not set, and `release.yml` passes `--no-grain-sandbox`. |

## Workflows and jobs

All jobs run on `ubuntu-latest` unless the table says otherwise.
`gke-labs/granule` is a private repository, and for private repositories
GitHub's standard `ubuntu-latest` runner is **2 vCPU / 7 GB RAM / about
14 GB free disk**, not the 4 vCPU / 16 GB that public repositories (such
as `ai-on-gke/grain`) get. `/dev/kvm` is present (`crw-rw---- root:kvm`)
and the udev rule opens it.

### `ci.yml`: per pull request and push to `main`

Replaced grain CI's `.grain/workflows/ci.yml`, `rootless-vm.yml` and
`sandbox-access-vm.yml`. Those last two were separate files only because
grain gives a job that names a runner no `/dev/kvm`.

- **Triggers:** `push` to `main`, `pull_request`, and `workflow_dispatch`
  with an optional `jobs` input: comma-separated job IDs such as
  `rootless-vm,sandbox-access-vm`. An empty input runs everything; an
  unknown job ID fails the `changes` job.
- **Concurrency:** one group per pull request, or per ref for `main` and
  dispatches. A newer push cancels the PR's run in progress. `main` is
  never cancelled.
- **Path filtering:** the `changes` job diffs the PR against its base and
  decides each job. Changes only to `docs/`, `*.md`, `LICENSE` or
  `.grain/` run nothing. A change to `ci.yml` itself, any push to `main`,
  and an empty dispatch run every job.

| Job ID | Check name | What it runs | Runs on a PR when | KVM |
| :--- | :--- | :--- | :--- | :--- |
| `changes` | `changes` | The path filter and dispatch selection | always | no |
| `test` | `test` | Base-image pin lint, gofmt, build, vet, `go test ./...`, arm64 vet and qemu-user tests, shell syntax, the demo and publish dry runs, the hosted-runner publish check, and root netns tests (`/dev/net/tun`, sudo) | anything other than docs changes | no |
| `published-rootfs` | `published guest rootfs` | Anonymous pull of `ghcr.io/gke-labs/granule/<variant>.rootfs:latest` and a consumer guest build on it. It warns rather than fails while `REQUIRE_PUBLISHED_ROOTFS` is `false`. It needs `test`, but still runs when a dispatch picks it alone. | `cmd/`, `pkg/`, `vendor/`, `go.mod`/`go.sum`, `Dockerfile`, `deploy/` | no |
| `rootless-vm` | `rootless VM container` | **Gated on `ENABLE_GUEST_IMAGE_TESTS`.** `test/rootless-vm.sh` with `REQUIRE_KVM=1`. It boots two 512 MiB guests rootless (user namespaces, rootless crun, subuids, virtiofsd). `/dev/kvm` is set to `0660 root:kvm`, the runner user is added to `kvm`, and the script runs under `sudo -u "$USER"` so that group membership applies. That keeps the shim's no-group refusal check meaningful; at mode `0666` it would skip. AppArmor's unprivileged user namespace restriction is lifted. | Go paths or `test/rootless-vm.sh` | yes |
| `sandbox-access-vm` | `sandbox access VM` | **Gated on `ENABLE_GUEST_IMAGE_TESTS`.** `test/sandbox-access-vm.sh`: a guest booted under the shim with a TLS access listener, driven from a peer netns | Go paths or `test/sandbox-access-vm.sh` | yes (udev `0666`) |

### `e2e-ipv6.yml`: nightly IPv6 on docker and kind

`test/e2e-ipv6.sh docker kind`: builds the alpine guest from this tree,
compiling the kernel, then checks it on a dual-stack and a v6-only docker
network, and on dual-stack and v6-only kind clusters. Job `e2e-ipv6`
(check name `IPv6 on docker and kind`), 90-minute timeout.

- **Triggers:** schedule (03:30 UTC), `workflow_dispatch`, and
  `pull_request` when the PR edits `test/e2e-ipv6.sh` or this workflow.
- **Hosted-runner setup:** it frees disk (dotnet, Android, GHC, CodeQL,
  Swift) because a cold guest build plus two kind clusters outgrows 14 GB.
  It also applies the udev KVM rule and loads `tun`. `kind` and `kubectl`
  come preinstalled.
- **Why it's off the per-PR path:** a cold kernel build and four VMs on
  two clusters. This is the same reason grain CI ran it nightly.
- **The `x509` failures:** these were grain's sandbox proxy, whose CA a
  kind node doesn't trust. A hosted runner pulls kind's images directly.
- **Measured on this repository's 2 vCPU / 7 GB runner (PR #240):** 24
  passed, 0 failed, 1 skipped. The skip is `[docker v6only] the guest
  resolves example.com through its own resolver`, because a hosted runner
  has no IPv6 default route; that is the script's documented skip, not a
  failure. The script took 17 minutes, cold build included, with 38 GB
  free after the disk cleanup.

### `e2e-ipv6-gke.yml`: IPv6 on dual-stack GKE (by hand)

Job `e2e-ipv6-gke` (`IPv6 on dual-stack GKE`), `workflow_dispatch` only,
gated on the GCP variables by a `gcp-credentials` job. It frees disk and
installs `gke-gcloud-auth-plugin` when missing.

### `gce-build.yml`: images build on a stock GCE VM

Job `gce-build` (`images build on a GCE VM`) runs `test/gce-build.sh
guests`. It's gated on the GCP variables.

- **Triggers:** `push` on every branch and `workflow_dispatch`. There is
  deliberately no `pull_request` trigger: the job holds GCP access and
  runs scripts from the tree, and a fork's PR must not reach it.
- **Dropped step:** grain's copy also checked that `publish-images.sh`
  recognises a grain sandbox, which can't be true here. `ci.yml`'s `test`
  job now checks the converse.

### `storage-benchmark.yml`: storage benchmark on GCE (by hand)

Job `storage-benchmark` runs `benchmarks/storage/gce-bench.sh` on an
n2-standard-16 VM. It's `workflow_dispatch` only and gated on the GCP
variables.

### `disk-direct-benchmark.yml`: CHV_DISK_DIRECT on vs off (by hand)

Job `disk-direct` runs `benchmarks/storage/gce-bench.sh --disk-direct`
(`GCE_MAX_RUN=3h`) on an n2-standard-16 VM, with a 90-minute timeout. It's
`workflow_dispatch` only and gated on the GCP variables.

### `release.yml`: publish guest images

Job `publish` (`publish guest images`) runs on `release: published`.
Grain's copy said `branches: [main]`, a filter the release event doesn't
support. The job has `packages: write`, frees disk, and runs
`deploy/publish-images.sh --no-grain-sandbox guests` with `GHCR_USER` /
`GHCR_TOKEN` taken from `github.actor` / `GITHUB_TOKEN`, or from
`GHCR_USERNAME` / `GHCR_TOKEN` when that secret is set.

### `guest-suites.yml`, `base-images.yml`: weekly drift checks

These are unchanged apart from pinned actions, and `guest-suites.yml`
now passes `matrix.suite` to its scripts as `$SUITE` rather than
splicing it in.

- `guest-suites.yml`: matrix job `guest-suite` (`guest bullseye`, `guest
  bookworm`, `guest trixie`). It runs on Mondays and on dispatch, where a
  `suite` input picks one suite. Its concurrency is never cancelled.
- `base-images.yml`: job `base-images` runs `deploy/base-images.sh check`
  on Mondays and on dispatch.

### `github-runner-images.yml`: self-hosted (gated)

Job `build` needs about 250 GB of scratch space, which no hosted runner
has. It runs only when `ENABLE_SELF_HOSTED_TESTS` is `true`, on the
`SELF_HOSTED_RUNNER` labels. Otherwise the `self-hosted` job posts a
notice and `build` is skipped rather than queued for a runner that
doesn't exist. The weekly schedule and the `image` / `runner_images_ref`
dispatch inputs are unchanged.

## Gated jobs

| Job | Gate | Why |
| :--- | :--- | :--- |
| `rootless-vm`, `sandbox-access-vm` | `ENABLE_GUEST_IMAGE_TESTS == 'true'` | Their published guest image isn't readable with this repository's `GITHUB_TOKEN` until the package grants access (checklist item 3). |
| `gce-build`, `storage-benchmark`, `disk-direct`, `e2e-ipv6-gke` | `GCP_WIF_PROVIDER` and `GCP_SA_EMAIL` | They need GCP credentials. They show as skipped, with a notice, until WIF is configured. |
| `github-runner-images` `build` | `ENABLE_SELF_HOSTED_TESTS == 'true'` | About 250 GB of scratch disk, against about 14 GB on a hosted runner. |

Every other job runs on hosted runners as-is. No VM size had to be scaled
down: the per-PR VM tests already boot 512 MiB guests, which fit the
7 GB runner.

One test needed a fix to run on a hosted runner. `cmd/granule-agent`'s
`TestGranuleAgentRun` assumed `run()` fails at the vsock socket off a
guest. A hosted runner is a Hyper-V VM where `AF_VSOCK` works, so `run()`
listened and its stop called `os.Exit(0)` inside the test binary. The
exit is now an injectable `exit` variable, and the test accepts either
outcome.

## zizmor

The organisation scans workflows on every pull request with zizmor (the
`zizmor-*` checks). `.github/workflows/` scans clean (`zizmor --offline
.github/workflows/`): every action is pinned to a commit SHA, checkouts
don't persist credentials, and no expression is spliced into a `run:`
script. Keep it that way when editing.

## Review of failed scheduled runs

Grain used to file a review task from the schedule triggers in
`.grain/workflows/`. Now each scheduled Actions workflow has a
`.grain/schedules/<name>-review.yml` declaration with a
`github_workflow_run` trigger instead. It fires when a run of that
workflow on `main` concludes `failure`, `timed_out` or `startup_failure`,
and the review task reads the run's logs from grain.

| Declaration | Workflow | Its schedule |
| :--- | :--- | :--- |
| `e2e-ipv6-review.yml` | `e2e-ipv6.yml` | daily, 03:30 UTC |
| `base-images-review.yml` | `base-images.yml` | Mondays |
| `guest-suites-review.yml` | `guest-suites.yml` | Mondays |
| `github-runner-images-review.yml` | `github-runner-images.yml` | weekly (gated on the self-hosted builder) |

A new scheduled workflow needs a declaration of its own to be reviewed.

## Reading Actions runs from grain

With `ci_mode: github`, `grain ci list --provider github-actions`,
`grain ci show` and `grain ci log` read this repository's Actions runs,
and `grain ci wait` waits on the GitHub checks that gate the branch.
`grain ci retrigger` and `grain ci run-job` are grain-CI-only and don't
apply here.
