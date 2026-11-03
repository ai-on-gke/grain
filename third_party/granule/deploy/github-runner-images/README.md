# GitHub runner environments

Granule packages GitHub's four x86-64 Ubuntu environments as bootable
guest images:

| GitHub label | Granule tag | Official source |
| --- | --- | --- |
| `ubuntu-22.04` | `github-ubuntu-22.04` | GitHub's Ubuntu 22.04 Packer definition |
| `ubuntu-24.04` | `github-ubuntu-24.04` | GitHub's Ubuntu 24.04 Packer definition |
| `ubuntu-latest` | `github-ubuntu-latest` | Alias of the same 24.04 image |
| `ubuntu-26.04` | `github-ubuntu-26.04` | GitHub's Ubuntu 26.04 Packer definition |
| `ubuntu-slim` | `github-ubuntu-slim` | GitHub's Ubuntu Slim Dockerfile |

The ARM images are deliberately outside v1. GitHub has no implicit
default runner; `ubuntu-latest` is its moving Ubuntu label and currently
resolves to Ubuntu 24.04.

Run a build from the repository root:

```sh
AZURE_RESOURCE_GROUP=granule-runner-images \
AZURE_LOCATION=eastus \
OUTPUT_IMAGE=ghcr.io/gke-labs/granule/github-ubuntu-24.04:latest \
PUSH=true \
deploy/github-runner-images/build.sh ubuntu-24.04
```

`RUNNER_IMAGES_REF` may be a commit, tag or branch and defaults to
`main`. Every image records the resolved upstream commit as an OCI label
and receives an additional tag ending in its first twelve characters.
That makes a scheduled moving build traceable without making the moving
tag itself pretend to be reproducible.

## Why these are separate heavyweight builds

GitHub publishes `ubuntu-slim` as an OCI Dockerfile. Its full Ubuntu
environments are instead produced as 75 GB Azure VM images by Packer.
Granule therefore runs GitHub's Packer definitions unchanged, exports the
managed disk, flattens its root filesystem into Granule's partitionless
ext4 layout, and installs Granule's guest agents and boot units. This is
not an approximation assembled from an installed-software list.

The builder needs an authenticated Azure CLI session, an existing
resource group, Docker, Packer, Azure CLI, AzCopy, libguestfs
(`guestfish`), `rsync`, and `e2fsprogs`. Budget at least 250 GB of free
temporary space. `ubuntu-slim` needs no Azure access but uses the same
conversion and packaging path.

Intermediate Azure images and disks and the local temporary directory
are deleted after the build. Set `KEEP_WORK=true` to retain them when
debugging a failed conversion.

Before publishing, every build runs `test-image.sh` against the locally
tagged image. The gate boots and restarts the guest through Granule, waits
for systemd, verifies the expected Ubuntu release and x86-64 architecture,
checks the `runner` account and Granule services, exercises DNS, and confirms
the core GitHub toolchain is on `PATH`. A failed check prevents all moving
and immutable tags from being pushed.

The `GitHub runner images` workflow runs this matrix weekly and can be
dispatched for one image. It deliberately uses the self-hosted labels
`linux`, `x64`, and `granule-image-builder`; a builder with that label must
provide the dependencies and temporary space above. Configure
`AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`,
`AZURE_RUNNER_IMAGE_RESOURCE_GROUP`, and
`AZURE_RUNNER_IMAGE_LOCATION` as repository variables. Azure login uses
GitHub OIDC, so no long-lived Azure client secret is stored in GitHub.

The imported image keeps GitHub's `runner` user, tool cache and installed
software. Conversion removes Azure-specific network and machine
identity, rewrites the root mount for Granule's `/dev/vda`, and regenerates
the initramfs after installing Granule's stable `eth0` naming rule.

## Running under Actions Runner Controller

Each image boots straight into the GitHub Actions runner agent, so it can
be the `runner` container of an
[Actions Runner Controller](https://github.com/actions/actions-runner-controller)
(`gha-runner-scale-set`) runner pod. The contract between the two:

- **ARC's side.** ARC registers each ephemeral runner just in time and
  hands the registration to the pod's `runner` container as the
  `ACTIONS_RUNNER_INPUT_JITCONFIG` environment variable. It expects that
  container to run the agent and to exit when the job is done, at which
  point it deletes the pod and its runner.
- **The VM container.** The image's entrypoint is granule's, which boots
  the guest; keep it. ARC's default runner template sets
  `command: ["/home/runner/run.sh"]`, a path that exists only inside the
  guest, so override it: leave `command` unset and give
  `args: ["run"]`. The container needs `/dev/kvm` and `/dev/net/tun`
  (for example the `devic.es/kvm` and `devic.es/tun` device-plugin
  resources) and `NET_ADMIN`. experimental/grain's
  `deploy/terraform/github-runner-cluster` module sets all of this.
- **Into the guest.** The image sets
  `GRANULE_WORKLOAD_ENV=ACTIONS_RUNNER_INPUT_JITCONFIG`. Granule passes
  each variable that list names, when it is set on the VM container, into
  the guest workload's environment, on the same read-only override disk
  that carries `CHV_CONTAINER_OVERRIDE`. The disk's image lives in the
  container's writable layer and the merged spec in the guest's
  `/run/granule/container.json` (mode 0600, root), so the registration
  stays inside the pod and the VM. Neither granule nor the runner prints it.
- **In the guest.** The workload is `/etc/granule/container.json`, run by
  `granule-container` as the `runner` user. Its entrypoint,
  `/usr/local/libexec/granule-actions-runner`, loads GitHub's
  `/etc/environment` (the tool cache, `ImageOS` and the full `PATH`),
  waits up to a minute for the Docker daemon, and execs
  `/home/runner/actions-runner/run.sh`. The runner reads the JIT config
  from its environment, registers, runs one job and exits.
  `RUNNER_MANUALLY_TRAP_SIG=1` makes `run.sh` pass the shutdown signal on
  to the runner when the pod is deleted mid-job.
- **Ending.** The workload has `haltOnExit`, so the VM powers off when
  `run.sh` exits. The VM container then exits with `run.sh`'s status, and
  so does the pod.

Started without `ACTIONS_RUNNER_INPUT_JITCONFIG`, for example by
`docker run` or by `test-image.sh`'s first boot, the workload logs that it
is not starting the runner and waits. The VM stays up for `granule exec`,
as it did before the image had a workload.

The agent is the [actions/runner](https://github.com/actions/runner)
release named by `ACTIONS_RUNNER_VERSION` at build time. It defaults to the
latest release, because GitHub refuses runners that fall too far behind,
and is recorded in the `io.granule.github-runner.actions-runner-version`
label. GitHub's own images do not include it: a hosted VM receives it when
it is provisioned. It is unpacked at `/home/runner/actions-runner`, owned
by `runner`, and its `installdependencies.sh` adds any native libraries
the environment lacks.

`test-image.sh` checks both sides. On the first boot it requires the
runner to be installed, startable as `runner` (`Runner.Listener
--version`) and waiting under an active `granule-container.service`. It
then starts a second container with a placeholder
`ACTIONS_RUNNER_INPUT_JITCONFIG`. The runner rejects that config without
contacting GitHub, and `run.sh` exits 0 as it does after a finished job.
The check requires the guest to log that it started the runner, `run.sh`
to run to its end, and the container to stop by itself with status 0
within five minutes.
