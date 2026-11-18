# Grain CI (while the Actions budget is spent)

The `gke-labs` organisation's GitHub Actions budget is exhausted, so this
repository's merge gate is **grain CI** for now: the deployment's per-repo
setting in grain/config is `ci_mode: grain`.

There is no `.grain/workflows/`, and there should not be one. With none,
grain CI runs `.github/workflows/*.yml` itself, with `act`, under the same
job names, so the required checks are the same as on Actions and a
workflow change belongs in `.github/workflows/` only, as
[github-actions-ci.md](github-actions-ci.md) says. A `.grain/workflows/`
would replace those jobs with its own, and grain refuses such a branch: a
branch cannot remove checks its base requires.

## What actually runs

Grain CI runs each job on its own, so no job sees another's outputs: a
job whose `if` reads `needs.<job>.outputs` sees an empty string there,
and a skipped job is reported as a pass. `ci.yml`'s `test` job therefore
tests `!= 'false'` rather than `== 'true'`, and skips `actions/setup-go`
and the hosted-runner-only publish check under act (`env.ACT`). The rest
skip under grain CI, by design for now:

| Job | Why it is skipped under grain CI |
| :--- | :--- |
| `changes` (all workflows) | Runs, but its outputs reach no other job. |
| `test` | **Runs**: base-image pin lint, gofmt, build, `go vet`, unit tests, arm64 vet and qemu-user tests, shell syntax, the demo and publish dry runs, netshim kernel tests as root. |
| `published guest rootfs` | Pulls and builds guest rootfs images: the heavy writes that hit the guest-kernel ext4 writeback hang in grain sandboxes (#263). |
| `rootless VM container`, `sandbox access VM` | Gated on `vars.ENABLE_GUEST_IMAGE_TESTS`, which act does not have, and act's job container has no `/dev/kvm`. |
| `IPv6 on docker and kind`, `ext4 buddy-cache eviction (debian12 kernel)` | Heavy path-filtered lanes (guest kernel builds, kind clusters) exposed to the same ext4 hang. |
| `GCP credentials`, `images build on a GCE VM` | Need GCP credentials through Workload Identity Federation, which only an Actions job gets. |

So `test` is the check that means something on grain CI. The others are
still required by name, and pass as skips.

## Going back to Actions

Set `ci_mode: github` for `gke-labs/granule` in grain/config and make the
required checks the Actions ones from
[github-actions-ci.md](github-actions-ci.md#operator-checklist). Nothing
in the repository has to change.
