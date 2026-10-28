# Builds the granule OCI image: a single Go binary that either boots a
# cloud-hypervisor VM as PID 1 of a container ("run", the default) or sets
# up networking as an optional init container ("netshim"). The default
# "run" mode sets up its own network before boot. The image also carries
# cloud-hypervisor and a guest disk and kernel, so no node-local image
# cache or init container is needed. Docker/Kubernetes must still grant
# KVM and network device access. See README.md and deploy/guest-image/README.md.

# Every base image below is pinned by digest as well as named by tag:
# the tag is for whoever reads this file, the digest is what is pulled,
# so moving a tag on Docker Hub (or in a mirror in between) does not
# change the toolchain that builds granule or the userland a guest is
# built from. CI refuses a FROM without one (deploy/base-images.sh lint),
# and deploy/base-images.sh update is how they are bumped.
#
# The two bases whose version is a build arg take their digest as a
# second one. Override both together:
#
#   --build-arg GO_VERSION=1.23 \
#   --build-arg GO_DIGEST=$(deploy/base-images.sh digest golang:1.23-bookworm)
#
# Overriding the version alone would pull the default version's digest
# under the new tag, so the stages that use them check the image they got
# is the version they were asked for and fail the build if not.
ARG GO_VERSION=1.22
ARG GO_DIGEST=sha256:3d699e4d15d0f8f13c9195c0632a16702b8cbdece2955af1c23b37ae5d55a253
ARG CLOUD_HYPERVISOR_VERSION=v53.0
# GRANULE_KERNEL_VERSION pins the guest kernel baked into the final granule
# image (see the "build-kernel" stage below) -- a tag of
# cloud-hypervisor/linux, the same known-good, PVH-entry, virtio-pci/
# virtio-blk/virtio-net-enabled tree already used to validate direct
# kernel boot elsewhere in this repo (see the top-level README's
# "Validated on a real VM with nested virtualization"). build-kernel
# compiles that tag's source with its own ch_defconfig plus IPv6, rather
# than downloading the tag's release binary, which has none.
# GRANULE_KERNEL_SHA256 is the sha256 of that tag's source tarball
# (https://github.com/cloud-hypervisor/linux/archive/refs/tags/<tag>.tar.gz)
# and moves with it.
ARG GRANULE_KERNEL_VERSION=ch-release-v6.16.9-20260508
ARG GRANULE_KERNEL_SHA256=a2aaf2b459842810f3521cdfe8f5fa97bd816768019e184b6dafa0c7254d6f21
# GUEST_DISTRO selects the guest disk image baked into the final granule
# image (not the granule image's own base, which is always Alpine -- see
# the "final" stage below): "debian" (the default, via debootstrap) or
# "alpine" (via apk). Both produce a minimal operating system -- an init
# system, acpid, iproute2, e2fsprogs and kmod, and nothing of granule's
# own, which the guest-image stage lays in when it packs. It also selects
# which of the two overlays that pack lays down; see
# deploy/guest-image/README.md for what else differs between them.
ARG GUEST_DISTRO=debian
# GUEST_SUITE only applies when GUEST_DISTRO=debian. Any suite
# debootstrap can bootstrap works; bullseye, bookworm and trixie are the
# ones built and checked -- weekly, each of them built and packed by
# .grain/workflows/guest-suites.yml, which is where that claim is kept
# true and what a suite added here has to be added to. What a new suite
# is liable to change out from under this build is which packages
# --variant=minbase installs, so the guest-rootfs-debian stage names the
# ones a packed guest needs in --include rather than inheriting them from
# a priority set (see its own comments) -- and pack refuses a rootfs
# missing one anyway, whichever suite built it
# (deploy/guest-image/pack-guest).
ARG GUEST_SUITE=bookworm
# GUEST_ALPINE_VERSION only applies when GUEST_DISTRO=alpine.
ARG GUEST_ALPINE_VERSION=3.20
# GUEST_ALPINE_DIGEST is alpine:${GUEST_ALPINE_VERSION}'s digest; see the
# note on pinning at the top of this file.
ARG GUEST_ALPINE_DIGEST=sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
# GUEST_SETUP_SCRIPT, if set, is a shell script run inside the guest rootfs
# at build time -- the build-time counterpart to CHV_SETUP_SCRIPT, for
# customization that belongs in the image rather than in every boot of it.
# It holds the script's own text, not a path.
#
# deploy/guest-image/README.md's "Running a custom setup script" points at
# CHV_SETUP_SCRIPT instead, on the grounds that a build-time chroot has no
# running kernel or service manager to work with. That reasoning does not
# apply here: see the "guest-customized" stage below, which runs this as an
# ordinary container rather than under chroot. What CHV_SETUP_SCRIPT is
# still the right answer for is anything that has to observe the guest
# actually running; what this is for is installing packages and dropping in
# files, paid once at build time by everyone who pulls the image rather
# than once per host at first boot.
#
# Anything it writes lands in the guest disk image, so do not use it to
# place secrets -- it is as public as the image is.
ARG GUEST_SETUP_SCRIPT=""
# GUEST_DISK_MODE, if set, bakes the default disk mode into the image
# (overlay, persistent, readonly). Left empty -- the default -- the
# runtime default (overlay) or CHV_DISK_MODE at boot time applies.
ARG GUEST_DISK_MODE=""

# GUEST_KERNEL_PACKAGE, if set, names a distro kernel package to install
# into the guest rootfs (e.g. "linux-image-amd64" on Debian). Left empty
# -- the default, and what granule's own bundled guest uses -- nothing is
# installed and the guest boots the build-kernel stage's
# cloud-hypervisor/linux build baked into the final image.
#
# A guest whose workload needs a richer kernel config than that build
# carries wants one of its own: overlayfs, cgroup v2, bridge netfilter
# and veth -- i.e. running containers inside the guest -- are the usual
# reasons, and a kernel built for cloud-hypervisor's own CI is not where
# to look for them. Debian's stock linux-image-amd64 already sets
# CONFIG_PVH and ships virtio-pci/virtio_blk/virtio_net as modules, which
# is all direct kernel boot needs.
#
# It is installed in the "guest-customized" stage rather than through
# debootstrap's --include, and that ordering is the whole point: see that
# stage's own comment.
ARG GUEST_KERNEL_PACKAGE=""

# GUEST_DISK_EXTRA_MB adds headroom to the guest disk, on top of the 20%
# the guest-image stage sizes it with.
#
# That 20% is sized against the rootfs being packed, which is the right
# answer for an image whose guest is finished at build time: it is space
# for logs and runtime growth, not for installing anything.
#
# An image built to be *derived from* -- customized by
# `granule guest build`, which boots it and provisions the running
# guest -- needs room for what that provisioning installs, and by then
# the filesystem is packed and cannot grow. A guest with 200MB free
# fails an `apt-get install` of anything substantial with "You don't have
# enough free space in /var/cache/apt/archives/", after booting
# perfectly, which reads as a broken setup script rather than a disk that
# was sized before anyone knew what would go on it.
#
# It is not free, though, and the cost is easy to misjudge. `truncate`
# leaves the extra as a hole, and a pushed layer of zeros compresses to
# almost nothing -- but a docker layer is a tar, and extracting one
# materializes the hole. Every GB here is a GB on disk on every machine
# that pulls the image. Size it against what the guest actually installs
# rather than against what feels safe.
#
# The way to get real headroom without paying for it in the image is to
# grow the disk at boot instead, which CHV_DISK_SIZE_MB (granule's
# -disk-size-mb) now does end to end: it sizes the overlay before
# cloud-hypervisor opens it, and granule-growfs resize2fs's the guest's
# ext4 onto that space on the next boot. That keeps the image sized to
# its rootfs and makes headroom a per-VM setting, so what is left for
# this build arg is space that has to be inside the image itself --
# which is what `granule guest build` needs: it provisions the guest
# in CHV_DISK_MODE=persistent, writing through to disk.img, and
# CHV_DISK_SIZE_MB is overlay-only by design.
ARG GUEST_DISK_EXTRA_MB=0

# GUEST_DNS is the resolver baked into the guest image's own
# /etc/resolv.conf, written into the rootfs tree by the guest-image stage
# below, just before pack-guest turns that tree into a disk.
#
# It is not what fixes the build host's resolver leaking into the image.
# debootstrap copies the *build host's* resolv.conf into the rootfs, and
# a build host's resolver is routinely an address that only exists in its
# own network namespace -- docker's embedded 127.0.0.11, a cloud metadata
# service, a link-local stub. From a guest on a tap that is unroutable,
# so the image came out with open IP egress and no working DNS,
# differently broken depending on which machine built it. What settles
# that is the `rm -f /rootfs/etc/resolv.conf` both rootfs stages end
# with: the rootfs they publish makes no claim about a resolver at all.
#
# What this arg adds, on top of that removal, is a baseline for the guest
# *this file bundles* -- a resolver that is the same wherever the image
# was built, so a guest booted with no nameservers on its command line
# still resolves names rather than nothing. Setting it empty skips the
# write, leaving the bundled guest with no resolv.conf either, exactly
# like the published rootfs and like any rootfs `granule guest pack`
# packs. A comma-separated value names more than one resolver:
# --build-arg GUEST_DNS=1.1.1.1,9.9.9.9 writes the two nameserver lines
# in that order.
#
# This is only the baseline the image carries. What a guest actually
# resolves through is settled per boot, from the nameservers on its ip=
# kernel parameter (NETSHIM_DNS, granule's -dns), which
# overlay-common/usr/local/libexec/granule-configure-dns writes over this
# file -- so a deployment on a network where 8.8.8.8 is wrong changes it
# there rather than rebuilding this image.
ARG GUEST_DNS=8.8.8.8

# GUEST_READY_FILE, if set, is a path inside the guest that this image's
# own boot creates once it has finished booting -- the guest half of
# "granule ready -file" / GRANULE_READY_FILE (granule's
# `vm create -ready-file`). See the top-level README's "Readiness" and
# deploy/guest-image/README.md's "Declaring the guest ready".
#
# What it writes is the path itself, into /etc/granule/ready-file. The
# service that reads it ships in every guest and is *enabled* by the pack
# that finds that file there -- granule-ready.service after
# sysinit.target and granule-agent.service on debian, /etc/init.d/granule-ready "after *" in the
# default runlevel on alpine -- both of which just run
# overlay-common/usr/local/libexec/granule-declare-ready, which creates
# the path named here. So a guest built with
# --build-arg GUEST_READY_FILE=/run/granule/ready is one whose VM can be
# created with -ready-file /run/granule/ready and whose "vm wait" then
# waits for the end of the boot rather than for the agent, which answers
# in early boot, seconds ahead of it.
#
# Empty is the default and declares nothing at all, leaving the service
# installed and not enabled, for the same reason the check itself is
# opt-in: a guest that declares nothing must never become a VM that is
# never ready. The published images are built this
# way, so a stock guest is still ready at "the agent answers".
#
# What this can honestly declare is the end of the boot, which is all a
# reference guest -- which boots into nothing in particular -- has to
# say. A guest with a workload in it wants its own declaration, made by
# whatever knows the workload is serving; see the two service
# definitions' comments for where to hang it.
#
# /run is the natural home for the path: it is a tmpfs, so the
# declaration belongs to this boot rather than to the disk image, and a
# path on the disk would still be there, stale and lying, on the next
# boot.
ARG GUEST_READY_FILE=""

FROM golang:${GO_VERSION}-bookworm@${GO_DIGEST} AS build
ARG GO_VERSION
RUN case "$(go env GOVERSION)" in \
      "go${GO_VERSION}"|"go${GO_VERSION}".*) ;; \
      *) echo "GO_VERSION=${GO_VERSION} but GO_DIGEST is $(go env GOVERSION)'s image; pass the matching GO_DIGEST too" >&2; exit 1 ;; \
    esac
ENV GOPROXY=direct
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY pkg ./pkg
COPY vendor* ./vendor/
RUN --mount=type=cache,target=/go/pkg/mod [ -d vendor ] || for i in 1 2 3; do go mod download && break || sleep 3; done
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/granule-shim ./cmd/granule-shim
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/granule ./cmd/granule
# granule-mem-agent is the guest-side half of automatic memory hotplug
# (see pkg/memagent and cmd/granule-mem-agent's own doc comment):
# unlike granule itself, this one ships *inside* the guest disk image
# (the guest-parts stage below carries it, and a pack lays it in),
# not the outer scratch image, so it's built as its own small static
# binary rather than folded into granule-shim's mode dispatch.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/granule-mem-agent ./cmd/granule-mem-agent
# granule-agent is what "granule exec" talks to (see pkg/guestexec and
# cmd/granule-agent): it listens on the guest's vsock port and runs one
# command per connection. It ships inside the guest disk image for the
# same reason granule-mem-agent does, and is built the same way -- static,
# CGO-free, so one binary runs on both the Debian and the Alpine guest.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/granule-agent ./cmd/granule-agent
# granule-container runs the container a "granule wrap" image was built
# around, from inside the guest: it chroots into the wrapped image's
# filesystem and execs its entrypoint with its environment (see
# cmd/granule-container and pkg/guestwrap). Built static and CGO-free
# like the two agents above, and carried in the "guest-parts" stage below
# rather than installed into every guest -- a guest with no container in
# it has nothing for it to run.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/granule-container ./cmd/granule-container
# Symlinks (not copies, so the final stage's own /usr/local/bin/granule-shim is
# the only copy of the binary that ships) standing in for /bin/sh,
# /bin/bash and their /usr/bin equivalents -- see cmd/granule-shim's own doc
# comment and pkg/guestexec's ShellCommandLine for why: granule-shim
# itself tells these two apart from its six real modes by argv[0], so
# all that's needed image-side is for the names a shell would normally
# live at to resolve to it.
RUN mkdir -p /out/shims/bin /out/shims/usr/bin /out/shims/usr/local/bin \
    && for shim in /out/shims/bin/sh /out/shims/bin/bash /out/shims/usr/bin/sh /out/shims/usr/bin/bash; do \
         ln -s /usr/local/bin/granule-shim "${shim}"; \
       done \
    && ln -s /usr/local/bin/granule-shim /out/shims/usr/local/bin/granule \
    && ln -s /usr/local/bin/granule-shim /out/shims/usr/local/bin/kontur

# cloud-hypervisor publishes fully static release binaries but no
# published checksum file, so the expected sha256 for each architecture
# is pinned here and verified at build time.
#
# github.com is not always reachable directly: on grain's CI runners and
# sandboxes direct connections are reset, and github is served instead by
# grain's proxy cache. GITHUB_PROXY is that cache's address, and empty
# means an ordinary verified-TLS download from github.com. When it is
# set, the download goes to that address first (with -k, since the
# proxy's certificate is not github's -- the pinned sha256 is what vouches
# for the file) and falls back to the ordinary download if that fails.
# It is opt-in rather than guessed from /etc/resolv.conf: elsewhere the
# first nameserver is a router or appliance that may well answer on 443,
# and nobody asked for an unauthenticated connection to it. grain's own
# callers (deploy/publish-images.sh in a grain sandbox, and the
# .grain/workflows builds that reach these stages) pass it. build-kernel
# does the same for its source tarball.
#
# GITHUB_PROXY is declared in fetch-chv and fetch-kernel-source only, so a
# change to it re-runs the downloads and nothing else.
FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8 AS fetch-chv
ARG CLOUD_HYPERVISOR_VERSION
ARG TARGETARCH
ARG GITHUB_PROXY=
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /out
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) asset="cloud-hypervisor-static"; \
             sha256="448af3d4e59b22c2987f7df94c213ad40fb53a10d437e42b5ee6c4fce7c29ecc" ;; \
      arm64) asset="cloud-hypervisor-static-aarch64"; \
             sha256="f192b510eea1c710cbc439d716bb0573c223fc463dbe3e6523788a2b7ef62850" ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    url="https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/${CLOUD_HYPERVISOR_VERSION}/${asset}"; \
    ns="${GITHUB_PROXY}"; \
    fetched=""; \
    if [ -n "$ns" ]; then \
      resolve_args=""; \
      for h in github.com objects.githubusercontent.com release-assets.githubusercontent.com raw.githubusercontent.com api.github.com; do \
        resolve_args="${resolve_args} --resolve ${h}:443:${ns}"; \
      done; \
      if curl --connect-timeout 10 --retry 3 --retry-delay 2 -fsSL -k ${resolve_args} -o cloud-hypervisor "$url"; then fetched=1; fi; \
    fi; \
    if [ -z "$fetched" ]; then \
      curl --retry 10 --retry-max-time 600 --retry-connrefused --retry-all-errors -fsSL -o cloud-hypervisor "$url"; \
    fi; \
    echo "${sha256}  cloud-hypervisor" | sha256sum -c -; \
    chmod +x cloud-hypervisor

# The guest kernel baked into the final image at
# /var/lib/granule/guest/vmlinux (see the "final" stage below), so a
# direct-kernel-boot VM works out of the box the same way the bundled
# guest disk image already does -- see pkg/config's defaultKernel.
#
# It is built here, from the cloud-hypervisor/linux tree
# GRANULE_KERNEL_VERSION names, rather than downloaded as that tag's
# release binary. The release binary is `make ch_defconfig` and nothing
# else, and ch_defconfig leaves CONFIG_IPV6 unset on every architecture
# -- so a guest booted on it had no IPv6 stack at all (no
# /proc/net/if_inet6, and with CONFIG_MODULES unset no way to load one),
# and nothing netshim did about IPv6 could reach it (see
# docs/ipv6-dual-stack-design.md, section 1.1). The build is the same one
# the tag's own .github/workflows/release.yaml runs, with the fragment
# below merged over ch_defconfig:
#
#   - CONFIG_IPV6=y, the whole reason for the stage -- and with it
#     CONFIG_IPV6_SIT off, which olddefconfig would otherwise turn on
#     and which puts a sit0 tunnel device in every guest for nothing.
#   - CONFIG_IKCONFIG/CONFIG_IKCONFIG_PROC, so the next person asking
#     "what is this kernel built with" reads /proc/config.gz in a guest
#     (or scripts/extract-ikconfig on the file) instead of reverse
#     engineering a symbol table.
#
# Anything else a workload needs from its kernel is what
# GUEST_KERNEL_PACKAGE is for; keep this fragment to what granule itself
# depends on. The build fails if `make olddefconfig` drops any line of it
# (a dependency the fragment did not satisfy), so it cannot quietly ship
# without what it says.
#
# Same trust model as fetch-chv above: fetch-kernel-source downloads the
# source tarball, through GITHUB_PROXY the same way, and checks it
# against a pinned sha256 before build-kernel builds anything from it. Moving GRANULE_KERNEL_VERSION means
# moving GRANULE_KERNEL_SHA256 with it. The stage runs on the build
# platform and cross-compiles for TARGETARCH, so a multi-arch build does
# not compile a kernel under emulation. amd64 gets the ELF vmlinux (PVH
# entry point, what cloud-hypervisor's x86_64 --kernel wants); arm64 gets
# the raw Image (cloud-hypervisor's aarch64 --kernel wants a PE-format
# Image, not vmlinux) -- both are copied to the same "vmlinux" output name
# so the final stage's COPY, and CHV_KERNEL's default, don't need to know
# which architecture produced it.
#
# A kernel build costs minutes where the download cost seconds: about
# that on a cold build, and nothing once the layer is cached, since the
# build's only inputs are the tarball, the fragment and the platforms.
# KBUILD_BUILD_* are fixed (the timestamp to the tag's commit time,
# which is what the tarball's mtimes carry) so that two builds of one tag
# differ in as little as the toolchain allows.
#
# Changing this kernel does not touch an image that carries a snapshot
# (VM SNAPSHOT / VM SET snapshot=on): a restore replays the guest's RAM,
# kernel included, from the snapshot rather than booting vmlinux again
# (pkg/hypervisor's BuildArgs), so such an image keeps the kernel it was
# snapshotted under -- without IPv6, if that was this stage's old
# download -- until it is rebuilt.
FROM --platform=$BUILDPLATFORM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8 AS fetch-kernel-source
ARG GRANULE_KERNEL_VERSION
ARG GRANULE_KERNEL_SHA256
ARG GITHUB_PROXY=
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /out
RUN set -eux; \
    url="https://github.com/cloud-hypervisor/linux/archive/refs/tags/${GRANULE_KERNEL_VERSION}.tar.gz"; \
    ns="${GITHUB_PROXY}"; \
    fetched=""; \
    if [ -n "$ns" ]; then \
      resolve_args=""; \
      for h in github.com codeload.github.com objects.githubusercontent.com raw.githubusercontent.com api.github.com; do \
        resolve_args="${resolve_args} --resolve ${h}:443:${ns}"; \
      done; \
      if curl --connect-timeout 10 --retry 3 --retry-delay 2 -fsSL -k ${resolve_args} -o linux.tar.gz "$url"; then fetched=1; fi; \
    fi; \
    if [ -z "$fetched" ]; then \
      curl --retry 10 --retry-max-time 600 --retry-connrefused --retry-all-errors -fsSL -o linux.tar.gz "$url"; \
    fi; \
    echo "${GRANULE_KERNEL_SHA256}  linux.tar.gz" | sha256sum -c -

FROM --platform=$BUILDPLATFORM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8 AS build-kernel
ARG TARGETARCH
ARG BUILDARCH
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) cross_gcc="gcc-x86-64-linux-gnu" ;; \
      arm64) cross_gcc="gcc-aarch64-linux-gnu" ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    if [ "${TARGETARCH}" = "${BUILDARCH}" ]; then cross_gcc=""; fi; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
      bc bison flex gcc libc6-dev libelf-dev libssl-dev make ${cross_gcc}; \
    rm -rf /var/lib/apt/lists/*
WORKDIR /src
# COPY, not a bind mount or a fetch in this stage: its cache key is the
# tarball's content, so the build below is reused across a GITHUB_PROXY
# change (or none) as long as the bytes are the same ones the sha256
# vouched for.
COPY --from=fetch-kernel-source /out/linux.tar.gz /tmp/linux.tar.gz
RUN tar -xzf /tmp/linux.tar.gz --strip-components=1 && rm /tmp/linux.tar.gz
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) arch=x86_64; cross="x86_64-linux-gnu-"; target=vmlinux; image=vmlinux ;; \
      arm64) arch=arm64; cross="aarch64-linux-gnu-"; target=Image; image=arch/arm64/boot/Image ;; \
    esac; \
    if [ "${TARGETARCH}" = "${BUILDARCH}" ]; then cross=""; fi; \
    printf '%s\n' \
      'CONFIG_IPV6=y' \
      '# CONFIG_IPV6_SIT is not set' \
      'CONFIG_IKCONFIG=y' \
      'CONFIG_IKCONFIG_PROC=y' \
      > /tmp/granule.config; \
    export ARCH="$arch" CROSS_COMPILE="$cross" \
      KBUILD_BUILD_USER=granule KBUILD_BUILD_HOST=granule \
      KBUILD_BUILD_TIMESTAMP="$(date -u -r Makefile)"; \
    make ch_defconfig; \
    scripts/kconfig/merge_config.sh -m .config /tmp/granule.config; \
    make olddefconfig; \
    while read -r want; do \
      grep -qx "$want" .config || { echo "${want} did not survive make olddefconfig" >&2; exit 1; }; \
    done < /tmp/granule.config; \
    make -j"$(nproc)" "$target"; \
    mkdir -p /out; \
    cp "$image" /out/vmlinux; \
    cp .config /out/config

# >>> guest rootfs recipe: everything between these two markers decides
# what the published guest rootfs contains, and nothing outside them
# does. deploy/guest-rootfs-key.sh hashes this region (with its comments
# and blank lines removed, as the builder removes them) together with the
# handful of build args the stages below read, and deploy/publish-images.sh
# names the published rootfs after that hash -- so a rootfs is republished
# when its recipe changes rather than once per commit. Two consequences
# for whoever edits below this line:
#
#   - A new ARG read down here is a new input, and has to be added to
#     that script's ROOTFS_INPUTS. It refuses a build arg it has not been
#     told how to classify rather than keying on a stale set.
#   - A stage added or moved outside this region, but reaching the
#     published rootfs, would be invisible to the key. Keep the rootfs
#     stages here; the marker at the end of guest-rootfs-image is the
#     boundary, and granule's own guest-side parts live after it on
#     purpose (see the "guest-parts" stage).
#
# Builds the Debian variant of the guest rootfs baked into the final
# granule image: a minimal Debian rootfs via `debootstrap`. See the
# "alpine" variant below and deploy/guest-image/README.md for how the two
# compare.
#
# There is deliberately no sshd here any more. It was only ever installed
# so that "granule exec" had a way in, and exec reaches the guest over its
# vsock device now (pkg/guestexec) -- so the whole apparatus that
# served it goes with it: the daemon, its host keys, the account to log
# into, the authorized_keys file and the per-boot keypair that filled it.
# A guest that wants sshd for an operator's own interactive access
# installs it the way it installs anything else, in a setup script run by
# `granule guest build`.
FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8 AS guest-rootfs-debian
ARG GUEST_SUITE
RUN apt-get update && apt-get install -y --no-install-recommends \
      debootstrap ca-certificates wget \
    && rm -rf /var/lib/apt/lists/* \
    && printf "tries = 10\ntimeout = 30\nretry_connrefused = on\nread_timeout = 30\nwaitretry = 2\n" >> /etc/wgetrc \
    && printf '#!/bin/sh\nfor i in 1 2 3 4 5; do\n  /usr/bin/wget --tries=3 --timeout=15 --waitretry=1 "$@" && exit 0\n  sleep 1\ndone\nexit 1\n' > /usr/local/bin/wget \
    && chmod +x /usr/local/bin/wget
# kmod is listed for the same kind of reason udev is: --variant=minbase
# installs priority-required packages and nothing else, and /sbin/modprobe
# is not among them. granule-agent.service loads the virtio-vsock driver
# with it before starting, and while udev would eventually autoload the
# module from the device's own modalias, the agent starts early in boot
# on purpose -- ahead of udev settling -- so relying on that would mean
# every guest's exec path racing its own boot. Nothing here checks for
# it: what granule's guest-side half needs from an OS is stated once, by
# deploy/guest-image/pack-guest, which refuses a rootfs without modprobe
# whoever built it.
#
# e2fsprogs is listed for a reason worth stating separately, because it
# reads as redundant: granule-growfs runs resize2fs, and that came for
# free on bookworm and bullseye -- e2fsprogs is priority "required"
# there, so minbase installed it without being asked. It is priority
# "important" in trixie, which minbase does not install, so
# GUEST_SUITE=trixie produced an OS with no resize2fs in it at all and
# pack rejected it. A priority set is the distro's business and moves
# between suites; what a guest granule can pack needs does not, so this
# asks for the package rather than inheriting it. The same goes for
# anything pack comes to require of an OS: name it here rather than
# hoping the suite keeps supplying it -- of what the overlays run today,
# ip comes from iproute2 (named here already) and hostname, mawk, sed
# and coreutils are all still priority-required.
#
# The mirror is named over http, not https, and deliberately so: apt
# authenticates packages by signature, so nothing rests on the transport
# here, and debootstrap writes this URL verbatim into the rootfs's own
# sources.list -- which is what every later apt-get in the image (the
# GUEST_KERNEL_PACKAGE install below, any GUEST_SETUP_SCRIPT) then
# fetches through. Naming it over https makes all of that traffic
# unproxyable, so an http caching proxy in front of the build -- the kind
# CI hosts commonly offer via HTTP_PROXY, and which Debian's own default
# sources.list is written for -- is at best bypassed and at worst answers
# the tunnel request with a 405 and fails the build outright.
RUN for i in 1 2 3 4 5 6 7 8 9 10; do \
      rm -rf /rootfs && mkdir -p /rootfs && \
      debootstrap --variant=minbase \
        --include=systemd-sysv,iproute2,acpid,udev,kmod,e2fsprogs \
        "${GUEST_SUITE}" /rootfs http://deb.debian.org/debian && break || sleep 5; \
    done; \
    test -f /rootfs/etc/debian_version

# What this stage does *not* do is lay granule's own guest-side parts
# into the rootfs. The agents, the overlay trees and the units that start
# them go on at pack time, from the granule doing the packing -- see the
# "guest-parts" stage below, deploy/guest-image/pack-guest and
# `granule guest pack`. This stage builds an operating system and stops
# there, so that where granule's parts go inside a guest is written down
# once instead of twice.
#
# - systemd-udevd's own /lib/udev/rules.d/80-net-setup-link.rules renames
#   NICs to "predictable" names (e.g. virtio-pci's second NIC becomes
#   "ens3", not "eth1") based on each device's PCI slot -- masked here by
#   symlinking the same filename under /etc/udev/rules.d (which
#   overrides it) to /dev/null, so every NIC keeps the kernel's own
#   enumeration order (eth0, eth1, ...) instead. Both the "ip=" kernel
#   parameter the guest's first NIC boots with (see
#   pkg/netshim.GuestConfig) and granule-control-net's own
#   default second-NIC name assume that naming; without this, the
#   control link silently never comes up, since udev has already renamed
#   "eth1" to "ens3" by the time granule-control-net.service runs and
#   checks for it.
#
#   This one stays in the OS build rather than moving to pack with the
#   rest, and the reason is the initramfs: GUEST_KERNEL_PACKAGE is
#   installed in the "guest-customized" stage below, and the initramfs
#   its postinst generates snapshots whatever is in /etc/udev/rules.d at
#   that moment. A mask laid down later -- at pack time -- would be in
#   the root filesystem and not in the initramfs, which renames the NIC
#   before the real root is ever mounted. So it is part of how this OS
#   names its network devices, decided before anything is built against
#   it, and pack does not restate it.
# - /etc/resolv.conf is removed because debootstrap copies the build
#   host's, and a build host's resolver is routinely an address that
#   only exists in its own network namespace -- which is how the image
#   came out differently broken depending on which machine built it.
#   Removing it here rather than overwriting it is what leaves the
#   published rootfs carrying no claim about a resolver at all; the
#   baseline GUEST_DNS names is written by the guest-image stage below,
#   into the tree that becomes this image's own bundled guest. See
#   GUEST_DNS's comment above.
# - GUEST_READY_FILE is a *declaration*, not a part: it writes the path
#   the guest should create once it has finished booting into
#   /etc/granule/ready-file, and pack enables granule-ready.service in
#   any rootfs that carries one (see deploy/guest-image/pack-guest's
#   conditional_on). Keeping the path in a file rather than in the unit
#   is what lets a guest customized later move the declaration by
#   writing one line.
#
# GUEST_READY_FILE is declared here rather than at the top of the stage
# on purpose: an ARG's value is part of the cache key of everything after
# it, so declaring it up there would make a build that names a path
# rerun this stage's debootstrap rather than just this RUN.
ARG GUEST_READY_FILE
RUN set -eux; \
    ln -sf /dev/null /rootfs/etc/udev/rules.d/80-net-setup-link.rules; \
    rm -f /rootfs/etc/resolv.conf; \
    mkdir -p /rootfs/etc/systemd/system; \
    for unit in \
      systemd-logind.service \
      getty.target \
      systemd-timesyncd.service \
      apt-daily.timer \
      apt-daily-upgrade.timer \
      dpkg-db-backup.timer \
      e2scrub_all.timer \
      fstrim.timer; do \
        ln -sf /dev/null "/rootfs/etc/systemd/system/$unit"; \
    done; \
    if [ -n "${GUEST_READY_FILE}" ]; then \
      case "${GUEST_READY_FILE}" in \
        /*) ;; \
        *) echo "GUEST_READY_FILE must be an absolute path inside the guest, got '${GUEST_READY_FILE}'" >&2; exit 1 ;; \
      esac; \
      install -d /rootfs/etc/granule; \
      printf '%s\n' "${GUEST_READY_FILE}" > /rootfs/etc/granule/ready-file; \
      chmod 0644 /rootfs/etc/granule/ready-file; \
    fi

# Builds the Alpine variant of the guest rootfs, via `apk --root` instead
# of `debootstrap` -- the same technique Alpine's own official base image
# is built with, run from a live Alpine environment (this stage's own
# `/etc/apk`) targeting a fresh directory. `openrc`/`busybox` (both pulled
# in by alpine-base) stand in for `systemd-sysv` as PID 1 and service
# manager; see deploy/guest-image/README.md for how the two variants'
# overlays differ as a result. Neither carries sshd any more: exec
# reaches the guest over its vsock device (see the Debian stage above).
FROM alpine:${GUEST_ALPINE_VERSION}@${GUEST_ALPINE_DIGEST} AS guest-rootfs-alpine
ARG GUEST_ALPINE_VERSION
RUN case "$(cat /etc/alpine-release)" in \
      "${GUEST_ALPINE_VERSION}"|"${GUEST_ALPINE_VERSION}".*) ;; \
      *) echo "GUEST_ALPINE_VERSION=${GUEST_ALPINE_VERSION} but GUEST_ALPINE_DIGEST is Alpine $(cat /etc/alpine-release)'s image; pass the matching GUEST_ALPINE_DIGEST too" >&2; exit 1 ;; \
    esac
# --root only changes where packages are installed; apk still reads its
# own trusted keys and repository list from this stage's own /etc/apk
# (i.e. this Alpine image's), not /rootfs's, unless told otherwise -- so
# --keys-dir/--repositories-file are pinned explicitly here rather than
# relying on the default (which is this stage's own /etc/apk, correct by
# accident, not by being told to).
#
# e2fsprogs-extra, not just e2fsprogs: Alpine splits resize2fs (and
# dumpe2fs, e2image, ...) out of the base package, so `apk add e2fsprogs`
# alone would leave granule-growfs with nothing to run and every guest
# silently stuck at the size it was built with. Both are named rather
# than relying on the dependency between them; pack refuses a rootfs with
# no resize2fs in it whoever built it, which is where that requirement is
# stated. Debian's equivalent is one package rather than two, e2fsprogs
# being unsplit there, but it is named on debootstrap's --include just as
# explicitly: it used to arrive unasked at priority required, which
# stopped being true in trixie.
RUN sed -i 's|https://|http://|g' /etc/apk/repositories \
    && apk add --no-cache --initdb --root /rootfs \
      --keys-dir /etc/apk/keys --repositories-file /etc/apk/repositories \
      alpine-base acpid iproute2 util-linux \
      e2fsprogs e2fsprogs-extra
RUN printf 'http://dl-cdn.alpinelinux.org/alpine/v%s/main\nhttp://dl-cdn.alpinelinux.org/alpine/v%s/community\n' \
      "${GUEST_ALPINE_VERSION}" "${GUEST_ALPINE_VERSION}" > /rootfs/etc/apk/repositories

# alpine-baselayout ships root with an *empty* password ("root:::0:::::"),
# and Alpine's inittab starts gettys on tty1-6 that accept it: anyone at
# the guest's console logs in as root with no password at all. alpine-base
# also pulls in busybox-suid, whose setuid su is only kept from honouring
# the same empty password by busybox refusing blank passwords off a tty
# listed in /etc/securetty -- a build option, not a property of the
# guest. Debian's debootstrap already locks root ("root:*:"), so this
# brings the Alpine guest into line: every account with an empty password
# field is locked, and the build fails if one is left. `granule exec`
# does not use passwords and is unaffected.
RUN sed -i 's/^\([^:]*\)::/\1:*:/' /rootfs/etc/shadow \
    && if grep -q '^[^:]*::' /rootfs/etc/shadow; then \
         echo "an account in the Alpine guest rootfs still has an empty password" >&2; exit 1; \
       fi

# As in the Debian stage above, granule's own guest-side parts are not
# laid in here: the overlay, the agents and the OpenRC services that
# start them go on at pack time (deploy/guest-image/pack-guest). What is
# left is enabling the services *Alpine* ships, which nothing else will:
# nothing enables an OpenRC service on install the way Debian's
# deb-systemd-helper does, and the equivalent of a systemd unit's
# [Install] symlink is just a symlink of its own, from /etc/init.d/<svc>
# into a runlevel directory.
# - sysinit/boot: the baseline a busybox-init/OpenRC system needs to
#   reach a usable state at all (device nodes, /proc & /sys, hostname,
#   cleaning /tmp, syslog) -- Debian's systemd does the equivalent
#   internally, with nothing to enable by hand for it.
# - default: acpid, which is the OS half of the power-button handling
#   (the handler script itself is granule's and goes on at pack time),
#   and "local".
#
# Where granule's own services go in this scheme has moved to the init
# scripts themselves, as a "# granule-runlevel:" line pack reads -- see
# deploy/guest-image/overlay-alpine/etc/init.d and pack-guest's
# enable_service.
#
# GUEST_READY_FILE is declared here rather than at the top of the stage
# for the same cache reason as in the debian stage above: an ARG's value
# is part of the cache key of everything after it, and the `apk add`
# there has nothing to do with this one. It writes a declaration and
# nothing else; enabling granule-ready against it is pack's job.
ARG GUEST_READY_FILE
RUN set -eux; \
    for svc in devfs dmesg mdev hwdrivers; do \
      ln -sf /etc/init.d/"${svc}" /rootfs/etc/runlevels/sysinit/"${svc}"; \
    done; \
    for svc in hwclock modules sysctl hostname bootmisc syslog; do \
      ln -sf /etc/init.d/"${svc}" /rootfs/etc/runlevels/boot/"${svc}"; \
    done; \
    for svc in local acpid; do \
      ln -sf /etc/init.d/"${svc}" /rootfs/etc/runlevels/default/"${svc}"; \
    done; \
    if [ -n "${GUEST_READY_FILE}" ]; then \
      case "${GUEST_READY_FILE}" in \
        /*) ;; \
        *) echo "GUEST_READY_FILE must be an absolute path inside the guest, got '${GUEST_READY_FILE}'" >&2; exit 1 ;; \
      esac; \
      install -d /rootfs/etc/granule; \
      printf '%s\n' "${GUEST_READY_FILE}" > /rootfs/etc/granule/ready-file; \
      chmod 0644 /rootfs/etc/granule/ready-file; \
    fi; \
    rm -f /rootfs/etc/resolv.conf

# Selects between the two guest-rootfs-* stages above -- an indirection
# forced by BuildKit not resolving ARGs in COPY --from, only FROM.
FROM guest-rootfs-${GUEST_DISTRO} AS guest-rootfs

# Applies GUEST_SETUP_SCRIPT, if one was given, inside the guest rootfs.
#
# The rootfs is promoted to an image stage of its own first, so the script
# runs as an ordinary RUN -- with a real /proc and /dev, working network,
# and the rootfs as / exactly as it will look booted -- rather than under
# `chroot`. That distinction is what preserves the "no extra privileges
# beyond an ordinary docker build" property the guest-image stage below
# already relies on for `mke2fs -d`: bind-mounting /proc and /dev into a
# chroot needs CAP_SYS_ADMIN, which a docker build does not have, and
# without them apt postinsts, `systemctl enable` and `update-initramfs`
# variously misbehave or fail outright.
#
# Both rootfs variants provide the /bin/sh this RUN needs (debootstrap
# --variant=minbase on Debian, busybox via alpine-base on Alpine), so this
# works the same way for either GUEST_DISTRO. With no script given the RUN
# is a no-op, and the stage is just a copy of the rootfs.
FROM scratch AS guest-customized
COPY --from=guest-rootfs /rootfs/ /
# A scratch-derived stage inherits no PATH, and while the runtime supplies
# a default when an image config sets none, this stage is the one place
# where the whole point is running a caller's own script -- so it is set
# explicitly rather than left to that fallback.
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# Installs GUEST_KERNEL_PACKAGE, in the one stage where the rootfs is a
# running container rather than a directory. That is not incidental:
#
#   - debootstrap's --include would install the package before the
#     guest-rootfs-debian stage has finished configuring the OS, so the
#     initramfs its postinst generates would predate that. That matters
#     concretely: the udev rules masked there (80-net-setup-link.rules)
#     are snapshotted *into* the initramfs, and an initramfs carrying the
#     unmasked rules renames the guest's NIC away from eth0 before the
#     real root is ever mounted -- leaving the "ip=" spec naming a device
#     that no longer exists, and the guest unreachable, with nothing in
#     the boot log obviously about naming. Installing here means the
#     first initramfs generated is already correct, with no second
#     update-initramfs to remember. It is also why that one mask stayed
#     in the OS build when the rest of granule's guest-side half moved to
#     pack time: a mask laid down after this point is in the root
#     filesystem and not in the initramfs, which is too late.
#   - It is also why this runs before GUEST_SETUP_SCRIPT below: a setup
#     script that writes udev rules or modules-load.d entries of its own
#     is responsible for its own `update-initramfs -u`, but one that only
#     installs packages -- the common case -- needs nothing.
#
# Only Debian's apt is supported: the Alpine variant would need
# `apk add linux-virt` and a different initramfs tool, so it fails here
# with a message rather than a missing-binary error further on.
ARG GUEST_KERNEL_PACKAGE
RUN set -eux; \
    if [ -n "${GUEST_KERNEL_PACKAGE}" ]; then \
      command -v apt-get >/dev/null 2>&1 || \
        { echo "GUEST_KERNEL_PACKAGE needs GUEST_DISTRO=debian; this rootfs has no apt-get" >&2; exit 1; }; \
      export DEBIAN_FRONTEND=noninteractive; \
      apt-get update; \
      apt-get install -y --no-install-recommends ${GUEST_KERNEL_PACKAGE} klibc-utils; \
      rm -rf /var/lib/apt/lists/*; \
      ls /boot/vmlinuz-* >/dev/null 2>&1 || \
        { echo "${GUEST_KERNEL_PACKAGE} installed but left no /boot/vmlinuz-*" >&2; exit 1; }; \
    fi

ARG GUEST_SETUP_SCRIPT
RUN if [ -n "${GUEST_SETUP_SCRIPT}" ]; then \
      printf '%s\n' "${GUEST_SETUP_SCRIPT}" > /tmp/granule-guest-setup.sh; \
      chmod 0755 /tmp/granule-guest-setup.sh; \
      /tmp/granule-guest-setup.sh; \
      rm -f /tmp/granule-guest-setup.sh; \
    fi

# The same rootfs as an artifact in its own right, so a consumer can
# write `FROM` against it and customize the guest with an ordinary
# Dockerfile, in their own repository and their own CI:
#
#     FROM ghcr.io/gke-labs/granule/debian.rootfs
#     RUN apt-get update && apt-get install -y whatever
#
# That reference is this target, published by deploy/publish-images.sh:
# separate packages per base OS (granule/debian.rootfs, granule/alpine.rootfs,
# granule/debian12.rootfs) under :latest. The immutable tag beside
# each of those is not the packed guest's :<commit> but
# :os-<key>, where the key is a hash of the recipe above --
# this region's text and the build args it reads -- so it changes when
# the operating system does and not when granule does. Building it here
# is for changing it, or for a rootfs with this build's own
# GUEST_SETUP_SCRIPT already in it:
#
#     docker build --target guest-rootfs-image -t granule-guest-rootfs .
#
# The point of publishing it is what it costs to publish the *disk*
# instead. disk.img is one large file in a single layer, so every version
# of a customized guest is a full disk in the registry whatever changed,
# pulled in full and retained in full. A rootfs image is ordinary Docker
# layers: a rebuild that changes one file pushes one small layer, and the
# disk becomes a local derived artifact, packed from the rootfs by
# whoever is about to boot it.
#
# Packing late is worth more than the dedup, though, because it is what
# lets the parts that must not come from a consumer stay the packer's:
# the guest-side agents and the overlay go on at pack time, over the
# consumer's rootfs, from the packer's own current build -- so a guest
# somebody published months ago boots with today's agent in it, with no
# rebuild on their side and no code of theirs executed to put it there.
# See docs/guest-images.md for the verbs that assumes ("pack", "rebase")
# and where they are up to.
#
# This stage adds nothing to guest-customized: it *is* that rootfs, with
# labels naming it, because the stage a consumer depends on should be one
# they can identify rather than an internal name that happens to be
# reachable.
FROM guest-customized AS guest-rootfs-image
LABEL org.opencontainers.image.title="granule-guest-rootfs" \
      org.opencontainers.image.description="The granule guest filesystem as an OCI image: a base to write FROM when customizing a guest, packed into a bootable disk by whoever runs it" \
      org.opencontainers.image.source="https://github.com/gke-labs/granule"
# <<< guest rootfs recipe ends here. What follows is granule's own
# guest-side half, which is laid over a rootfs at pack time and is
# deliberately not part of the rootfs or of its content key.

# Export the two static guest-side programs without also exporting a
# rootfs. Image importers can place them into a foreign rootfs before it
# is packed into disk.img.
FROM scratch AS guest-agent-artifacts
COPY --from=build /out/granule-agent /granule-agent
COPY --from=build /out/granule-mem-agent /granule-mem-agent

# Everything granule lays over somebody else's guest rootfs when it packs
# one, in one directory: the two agents above, the overlay trees, and
# pack-guest, the script that does the laying down and runs `mke2fs -d`.
#
# This is the only statement anywhere of what granule's guest-side half
# consists of and where it goes. The guest-rootfs-* stages above build an
# operating system and stop; every guest -- a consumer's, and the one
# this file bundles, packed by the guest-image stage below out of exactly
# this directory -- gets granule's parts from a pack, at pack time, from
# the granule doing the packing. That is what lets a rootfs published
# months ago boot with the current agent in it, without running any of
# that publisher's code to put it there, and it is why a change to the
# layout is made once. See pkg/guestpack and docs/guest-images.md.
#
# It is copied into the runtime stage below (at pkg/guestimage's
# PartsDir) rather than published on its own, so that the agents in a
# packed guest and the granule that talks to them over vsock come from one
# image and cannot skew.
FROM guest-agent-artifacts AS guest-parts
# The guest-side runner `granule wrap` installs into a wrapped guest (see
# pkg/guestimage's PartsRunner). It rides along here rather than in
# the two artifact stages above because it is laid down by the wrap build
# rather than by pack-guest: only a guest with a container in it has
# anything for it to run.
COPY --from=build /out/granule-container /granule-container
COPY deploy/guest-image/overlay-common/ /overlay-common/
COPY deploy/guest-image/overlay-debian/ /overlay-debian/
COPY deploy/guest-image/overlay-alpine/ /overlay-alpine/
COPY deploy/guest-image/pack-guest /pack-guest

# Packs guest-customized (the rootfs GUEST_DISTRO selected, with
# GUEST_SETUP_SCRIPT applied to it) into disk.img -- by running
# pack-guest, the same script `granule guest pack` runs over a consumer's
# rootfs, over the same guest-parts directory. The guest this file
# bundles is packed the way every other guest is, rather than by a second
# implementation of it here, so the two cannot come to disagree about
# what granule puts inside a guest.
#
# What the script does, in order: lays granule's parts over the rootfs,
# enables the services it just installed, checks the OS provides the
# programs their boot runs (resize2fs, modprobe), stages the guest's own
# kernel pair if a GUEST_KERNEL_PACKAGE put one in /boot, copies the
# contract declaration out of the rootfs it just wrote it into, and
# `mke2fs -d`'s the lot into a disk sized to the rootfs plus 20% for
# logs/growth plus a 64MiB floor plus GUEST_DISK_EXTRA_MB. `mke2fs -d`
# populates a filesystem straight from a directory tree, so this needs no
# loop mount and no privileges beyond an ordinary docker build.
#
# GRANULE_GUEST_VARIANT is passed rather than left to the script's own
# detection: this build knows which rootfs stage it selected, and saying
# so means a guest whose setup script installed the other init system
# cannot be packed with the wrong overlay.
#
# GUEST_DNS is written here rather than by the pack, and before it: the
# baseline resolver belongs to *this image's own guest* and not to
# granule's guest-side half, so it is not something a pack of somebody
# else's rootfs should be laying down (see that arg's comment at the top
# of this file for what it is for, and why the rootfs stages remove the
# build host's resolv.conf rather than keeping it). Before, because the
# file has to be in the tree pack-guest measures and packs -- once the
# script has run there is a disk and no longer a directory to write
# into.
FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8 AS guest-image
RUN apt-get update && apt-get install -y --no-install-recommends e2fsprogs \
    && rm -rf /var/lib/apt/lists/*
COPY --from=guest-customized / /rootfs
COPY --from=guest-parts / /parts
ARG GUEST_DNS=8.8.8.8
ARG GUEST_DISK_EXTRA_MB
ARG GUEST_DISTRO
RUN set -eux; \
    if [ -n "${GUEST_DNS}" ]; then \
      : > /rootfs/etc/resolv.conf; \
      echo "${GUEST_DNS}" | tr ',' '\n' | while read -r ns; do \
        if [ -n "$ns" ]; then printf 'nameserver %s\n' "$ns" >> /rootfs/etc/resolv.conf; fi; \
      done; \
      chmod 0644 /rootfs/etc/resolv.conf; \
    fi; \
    GUEST_DISK_EXTRA_MB="${GUEST_DISK_EXTRA_MB:-0}" \
    GRANULE_GUEST_VARIANT="${GUEST_DISTRO}" \
      /bin/sh /parts/pack-guest /rootfs /parts /out

# Everything a caller needs to boot this guest elsewhere, and nothing
# else, so `docker build --target guest-artifacts --output type=local,dest=<dir>`
# yields exactly those files rather than a whole builder filesystem: the
# disk, the contract version it declares, and the kernel pair if it has
# one. The declaration has to travel with the disk wherever it is put --
# it describes that disk and nothing else (see pkg/guestimage's
# Files()).
FROM scratch AS guest-artifacts
COPY --from=guest-image /out/ /

# The guest-free runtime is also a public build target. Large derived
# guests (notably the GitHub runner environments under
# deploy/github-runner-images) build this once, then add their own disk,
# kernel and initramfs without first layering the default Debian guest
# underneath them.
#
# Guest-free means kernel-free as well, and that is the whole of what
# makes this target an incomplete half rather than a small one: the
# build-kernel vmlinux the "final" stage below copies in is *not* here,
# so an image built on this one boots only if its guest brings a kernel
# and initramfs of its own (a rootfs built with GUEST_KERNEL_PACKAGE, or
# the runner images' own). Packing, wrapping or rebasing an ordinary
# guest -- which brings neither -- onto this stage would produce an
# image that builds and then stops at
# "CHV_KERNEL (/var/lib/granule/guest/vmlinux): no such file or
# directory" at boot, so `granule guest pack`, `granule wrap` and
# `granule guest rebase` all refuse that combination at build time
# instead (see pkg/guestpack's KonturImageHasKernel). What to pack
# an ordinary guest onto is the full image below, whose bundled disk
# stays in the result's lower layers; that cost is reported and is the
# price of the kernel.
#
# netshim mode still needs CAP_NET_ADMIN/CAP_NET_RAW at runtime (creating
# the tap, the tc splice and the control link), but as of
# `pkg/netshim` talking to the kernel directly via the netlink Go
# library instead of exec'ing the `ip`/`tc` CLIs, it no longer needs a
# userland that provides those binaries -- so unlike before, this image
# doesn't need a real distro base at all. `scratch` (literally nothing
# but the two binaries, the guest-parts directory and the guest disk
# image below) is the smallest possible base; it costs the `ip`-based
# debuggability a real userland gave
# (no shell, no coreutils, no package manager to `apk add` a debug tool
# into), traded for the smaller image and attack surface. Both `granule`
# and `cloud-hypervisor` are statically linked (see the "build" and
# "fetch-chv" stages above), so neither needs a libc from the base image.
FROM scratch AS runtime
COPY --from=fetch-chv /out/cloud-hypervisor /usr/local/bin/cloud-hypervisor
COPY --from=build /out/granule-shim /usr/local/bin/granule-shim
# The guest-side pieces this granule lays over somebody else's rootfs when
# it packs one -- see the "guest-parts" stage above, pkg/guestimage's
# PartsDir, and `granule guest pack`. About 5MB of static binaries and a
# few dozen small files, carried by the VMM half so that "the current
# agents" and "the granule that talks to them" are one answer rather than
# two things to keep in step.
COPY --from=guest-parts / /usr/local/share/granule/guest-parts/
# See the "build" stage above -- these are symlinks back to the same
# /usr/local/bin/granule-shim binary just copied in, not real shells: this
# image is still "scratch" underneath (no coreutils, no package manager),
# it just also answers to "sh"/"bash" the way one would.
COPY --from=build /out/shims/ /

LABEL org.opencontainers.image.title="granule" \
      org.opencontainers.image.description="Boots a single cloud-hypervisor VM as PID 1 (\"run\", the default), sets up pod-local VM networking (\"netshim\"), or execs a command inside the VM guest over vsock (\"exec\")" \
      org.opencontainers.image.source="https://github.com/gke-labs/granule"

ENTRYPOINT ["/usr/local/bin/granule-shim"]

FROM runtime AS final
# The packed disk, the contract version it declares, and the guest's own
# kernel and initramfs when GUEST_KERNEL_PACKAGE put one in the rootfs --
# the same set guest-artifacts exports, which is everything pack-guest
# produced and nothing else. A guest that brought no kernel has none here
# and the build-kernel vmlinux below stays what boots it; pkg/config
# picks between the two by which files exist (see its defaultKernel) and
# refuses to boot at all if the declaration lands here stating a version
# this granule does not implement (see its checkGuestContract).
COPY --from=guest-image /out/ /var/lib/granule/guest/
# See the "build-kernel" stage above and pkg/config's defaultKernel:
# a direct-kernel-boot VM works out of the box, the same way the guest
# disk image above does, without anything needing to be supplied from
# outside the image (a node-local kernel cache, a hostPath, or an
# emptyDir populated by a fetch-at-pod-start init container).
COPY --from=build-kernel /out/vmlinux /var/lib/granule/guest/vmlinux

# Which guest-side contract the disk above implements -- the agents, the
# units that start them and the helpers the overlay places, as
# pkg/guestimage's Contract counts them. It goes on this stage and
# not on "runtime", because it describes the guest inside the image
# rather than the granule outside it: a "runtime" build carries no guest
# at all, and a guest packed onto one declares its own version (see
# pkg/guestpack). `granule guest rebase` reads this to tell a
# guest it may leave alone from one whose granule-side pieces predate
# something granule now requires; the same number is inside the rootfs at
# /etc/granule/guest-contract, placed by the overlay. The two, and
# guestimage.Contract, are held in step by that package's
# TestContractIsDeclaredEverywhere.
LABEL dev.granule.guest.contract="3"
ARG GUEST_DISK_MODE=""
ENV CHV_DISK_MODE=${GUEST_DISK_MODE}
