#!/usr/bin/env bash
# Builds the granule image every `granule granulefile build` in this
# repository packs its guests onto, out of the vendored tree in
# third_party/granule.
#
# It exists because `granulefile build` has a default for that image and
# the default cannot work here. A `VM PACK` stage generates a Dockerfile
# whose final stage is `FROM <granule image>` and which copies the
# guest-side pieces out of it -- `COPY --from=<granule image>
# /usr/local/share/granule/guest-parts/` -- and with no -kontur-image the
# reference written there is granule's own `granule:latest`
# (third_party/granule's pkg/vmspec.DockerImage, the tag granule's README
# tells its own readers to `docker build -t granule:latest .`). Nothing in
# *this* repository builds, pulls or tags that, and an unqualified name is
# a Docker Hub one, so every guest build here died at its first step:
#
#   #2 [internal] load metadata for docker.io/library/granule:latest
#   #2 ERROR: pull access denied, repository does not exist or may
#      require authorization
#
# So the reference is named rather than defaulted -- every caller passes
# `-kontur-image` -- and this is the one place that says what it is and
# how it is made. tests/deploy's
# TestEveryGuestBuildPacksOntoAGranuleImageThisRepositoryBuilds holds the
# callers to it.
#
# Built here, not pulled
# ----------------------
#
# The same two reasons the kontur base was built from the vendored tree
# before the guest pipeline became Granulefiles, and neither has moved:
#
#   - A GitHub Actions GITHUB_TOKEN is scoped to its own repository.
#     grain's can push grain's packages and cannot read granule's, so a
#     `docker pull` of a granule package from grain's CI is denied however
#     the login went.
#   - The granule that packs a guest and the guest-side pieces it lays
#     down have to agree on granule's guest contract
#     (third_party/granule's pkg/guestimage.Contract). Building from the
#     vendored tree makes "the granule and the guest are one commit" true
#     by construction, rather than by a pin somebody has to remember to
#     move -- which is also why `third_party/granule` is one of the paths
#     guest-inputs.sh names: this image is part of what comes out of the
#     guest build, so a vendored resync has to miss the guest cache.
#
# --target runtime
# ----------------
#
# The VMM half alone: granule-shim, cloud-hypervisor and the guest-parts
# the pack lays over a rootfs. A stock `docker build` of that tree would
# also debootstrap granule's *own* Debian guest and fetch a kernel for it,
# and this build has no use for either -- the rootfs being packed is the
# one the Granulefiles in this repository describe, and it installs
# linux-image-amd64, so it brings its own kernel and initramfs. Carrying
# granule's as well would be minutes of build time and a guest disk of
# somebody else's in the lower layers of every sandbox image this
# deployment pulls.
#
# That is granule's own documented split rather than a guess at one, and
# it fails loudly if this repository ever stops installing a kernel in its
# guests: `granule guest pack` refuses a rootfs that brings no kernel onto
# a runtime image, naming both halves of the choice (its
# RequireGuestKernel; "A `docker build --target runtime` granule image
# deliberately carries no kernel: it is the VMM half for guests that bring
# their own").
#
# Usage:
#   ./build-granule-image.sh                   # -> granule:vendored
#   IMAGE=granule:mine ./build-granule-image.sh
#
# Environment:
#   IMAGE               the tag to leave behind (default granule:vendored)
#   GUEST_SOURCE_REPO   relabel the result as belonging to this repository
#   GITHUB_PROXY        the vendored Dockerfile's build arg of that name:
#                       where its fetch-chv and fetch-kernel stages reach
#                       github.com. Default: grain's nameserver inside a
#                       grain sandbox, empty (direct, verified TLS)
#                       anywhere else; set it empty to force direct.
set -euo pipefail
cd "$(dirname "$0")/../.."

IMAGE="${IMAGE:-granule:vendored}"

# The vendored Dockerfile used to guess its github proxy from the build's
# /etc/resolv.conf; since gke-labs/granule#181 it goes there only
# when GITHUB_PROXY says so, and otherwise downloads from github.com
# directly -- which grain's sandboxes and CI runners cannot reach. So the
# guess moves here, gated the way granule's own deploy/publish-images.sh
# gates it: only on a machine carrying grain's CA, because anywhere else
# the first nameserver is a router that has no business being trusted
# with an unverified TLS download.
if [ -z "${GITHUB_PROXY+set}" ]; then
  GITHUB_PROXY=""
  for cand in /usr/local/share/ca-certificates/grain-ca.crt /etc/ssl/certs/grain-ca.pem; do
    if [ -f "$cand" ] && openssl x509 -in "$cand" -noout -subject 2>/dev/null | grep -q "Grain Internal Root CA"; then
      GITHUB_PROXY=$(grep -E '^nameserver' /etc/resolv.conf 2>/dev/null | awk '{print $2}' \
        | grep -v -E '^(8\.8\.8\.8|8\.8\.4\.4|127\.|0\.|169\.254\.)' | head -n 1 || true)
      break
    fi
  done
fi

echo "building ${IMAGE} from third_party/granule${GITHUB_PROXY:+ (github via ${GITHUB_PROXY})}"
DOCKER_BUILDKIT=1 docker build --target runtime -t "$IMAGE" third_party/granule \
  --build-arg "GITHUB_PROXY=${GITHUB_PROXY}"

# Everything packed onto this image inherits its labels, and one of them
# decides where a push lands: the vendored Dockerfile's
# org.opencontainers.image.source names *granule's* repository, and GHCR
# reads that label alone to decide which repository a package belongs to.
# A guest cached under grain's own namespace but still claiming granule's
# would be filed somewhere nobody is looking, silently, or refused
# outright by a token with no rights there. Left unset the vendored label
# stands, which is right for a local build and for the nightly comparison
# that pushes nothing.
if [ -n "${GUEST_SOURCE_REPO:-}" ]; then
  echo "labelling ${IMAGE} as ${GUEST_SOURCE_REPO}"
  workdir="$(mktemp -d)"
  trap 'rm -rf "$workdir"' EXIT
  printf 'FROM %s\nLABEL org.opencontainers.image.source=%s\n' "$IMAGE" "$GUEST_SOURCE_REPO" \
    | DOCKER_BUILDKIT=1 docker build -t "$IMAGE" -f - "$workdir"
fi

echo "built: ${IMAGE}"
