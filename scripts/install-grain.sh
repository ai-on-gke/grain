#!/usr/bin/env bash
# Install the grain binary from a release image, for one account, with no
# build: docs/quickstart.md step 1 in one command.
#
# Usage:
#   scripts/install-grain.sh -version vX.Y.Z [-key cosign.pub] [-dest DIR] [-image REPO]
#
#   -version  the release tag to install (required): a vX.Y.Z tag, never a
#             moving one, so what is installed is what was asked for
#   -key      the release's cosign public key: the image's signature must
#             verify against it (docs/image-signing.md) before anything is
#             installed. Without it, nothing is verified -- say so loudly
#   -dest     where the binary goes (default ~/.local/bin)
#   -image    the image repository (default ghcr.io/bwsalmon/grain/grain)
#
# It pulls the image, pins it to the digest the registry served, verifies
# that digest, copies /usr/local/bin/grain out of it, and prints the
# digest and the binary's sha256 to record beside the install.
set -euo pipefail

version=""
key=""
dest="${HOME}/.local/bin"
image="ghcr.io/bwsalmon/grain/grain"

while [ $# -gt 0 ]; do
  case "$1" in
    -version) version="${2:?-version needs a value}"; shift 2 ;;
    -key) key="${2:?-key needs a value}"; shift 2 ;;
    -dest) dest="${2:?-dest needs a value}"; shift 2 ;;
    -image) image="${2:?-image needs a value}"; shift 2 ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "install-grain.sh: unknown argument $1" >&2; exit 2 ;;
  esac
done

if ! [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "install-grain.sh: -version must be a release tag (vX.Y.Z), not '${version}'" >&2
  exit 2
fi
command -v docker >/dev/null || { echo "install-grain.sh: needs docker to pull the release image" >&2; exit 1; }

docker pull -q "${image}:${version}" >/dev/null
pinned="$(docker inspect --format '{{index .RepoDigests 0}}' "${image}:${version}")"
echo "image: ${pinned}"

if [ -n "$key" ]; then
  command -v cosign >/dev/null || { echo "install-grain.sh: -key needs cosign on PATH" >&2; exit 1; }
  cosign verify --key "$key" --insecure-ignore-tlog=true "$pinned" >/dev/null
  echo "signature: verified against ${key}"
else
  echo "signature: NOT verified (no -key given)" >&2
fi

container="$(docker create "$pinned")"
trap 'docker rm "$container" >/dev/null 2>&1 || true' EXIT
mkdir -p "$dest"
tmp="$(mktemp "${dest}/.grain.XXXXXX")"
docker cp "${container}:/usr/local/bin/grain" "$tmp"
chmod 0755 "$tmp"
mv -f "$tmp" "${dest}/grain"

sha256sum "${dest}/grain"
"${dest}/grain" version
case ":${PATH}:" in
  *":${dest}:"*) ;;
  *) echo "add ${dest} to your PATH" >&2 ;;
esac
