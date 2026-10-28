ARG GRANULE_RUNTIME_IMAGE=granule-runtime:local
FROM ${GRANULE_RUNTIME_IMAGE}

ARG GITHUB_RUNNER_LABEL
ARG RUNNER_IMAGES_REVISION
# The guest-side contract version the rootfs below implements, supplied
# by build.sh out of the overlay it laid into that rootfs rather than
# written down here -- see pkg/guestimage. It is only needed for the
# *label*, which a `docker build` can set no other way; the copy a boot
# reads travels with the disk as an ordinary file. It is what lets a
# later `granule guest rebase` of this image tell a guest it may leave
# alone from one whose agents predate what granule needs.
ARG GRANULE_GUEST_CONTRACT

# A guest half built here, dropped onto a VMM half built by granule: the
# four paths are granule's guest-image convention, stated in
# pkg/guestimage and explained in docs/guest-images.md, and they are
# fixed rather than chosen here. guest-contract is the declaration the
# granule above reads before it starts cloud-hypervisor; prepare-rootfs.sh
# lifts it out of the rootfs it packed, which is why it needs no build
# arg of its own the way the label below does. vmlinux -- granule's own kernel -- is
# deliberately not among them: it belongs to the runtime image above and
# moves when that does.
COPY disk.img /var/lib/granule/guest/disk.img
COPY guest-contract /var/lib/granule/guest/guest-contract
COPY vmlinuz /var/lib/granule/guest/vmlinuz
COPY initrd.img /var/lib/granule/guest/initrd.img

LABEL org.opencontainers.image.title="Granule GitHub runner environment (${GITHUB_RUNNER_LABEL})" \
      org.opencontainers.image.description="GitHub's ${GITHUB_RUNNER_LABEL} x86-64 environment packaged as a Granule guest" \
      org.opencontainers.image.source="https://github.com/gke-labs/granule" \
      io.granule.github-runner.label="${GITHUB_RUNNER_LABEL}" \
      io.granule.github-runner.runner-images-revision="${RUNNER_IMAGES_REVISION}" \
      dev.granule.guest.contract="${GRANULE_GUEST_CONTRACT}"
