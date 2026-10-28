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
