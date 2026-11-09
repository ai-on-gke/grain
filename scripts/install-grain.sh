#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Install the grain binary from a release, for one account, with no
# build: docs/quickstart.md step 1 in one command.
#
# Usage:
#   scripts/install-grain.sh -version vX.Y.Z [-dest DIR] [-repo OWNER/NAME]
#   scripts/install-grain.sh -version vX.Y.Z -image REPO [-key cosign.pub] [-dest DIR]
#
#   -version  the release to install (required): a vX.Y.Z tag, never a
#             moving one, so what is installed is what was asked for
#   -repo     the GitHub repository whose release assets carry the image
#             (default ai-on-gke/grain; docs/release-assets.md)
#   -image    pull the grain image from this registry repository instead
#             of release assets (CI publishes docker.io/grainai/grain)
#   -key      with -image, the release's cosign public key: the image's
#             signature must verify against it (docs/image-signing.md)
#             before anything is installed
#   -dest     where the binary goes (default ~/.local/bin)
#
# From release assets it downloads the release's images.json and its
# grain image tarball, checks the tarball against the sha256 images.json
# and its sidecar name, `docker load`s it and checks the loaded image ID.
# From a registry it pulls the image and pins it to the digest served.
# Either way it copies /usr/local/bin/grain out of the image and prints
# the image and the binary's sha256 to record beside the install.
set -euo pipefail

version=""
key=""
dest="${HOME}/.local/bin"
image=""
repo="ai-on-gke/grain"

while [ $# -gt 0 ]; do
  case "$1" in
    -version) version="${2:?-version needs a value}"; shift 2 ;;
    -key) key="${2:?-key needs a value}"; shift 2 ;;
    -dest) dest="${2:?-dest needs a value}"; shift 2 ;;
    -image) image="${2:?-image needs a value}"; shift 2 ;;
    -repo) repo="${2:?-repo needs a value}"; shift 2 ;;
    -h|--help) sed -n '15,40p' "$0"; exit 0 ;;
    *) echo "install-grain.sh: unknown argument $1" >&2; exit 2 ;;
  esac
done

if ! [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "install-grain.sh: -version must be a release tag (vX.Y.Z), not '${version}'" >&2
  exit 2
fi
command -v docker >/dev/null || { echo "install-grain.sh: needs docker to load the release image" >&2; exit 1; }

if [ -z "$image" ]; then
  # A path segment of every github.com URL below: OWNER/NAME and nothing
  # else, so "..", a query or a fragment cannot point the download at
  # another repository.
  if ! [[ "$repo" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || [[ "$repo" == *..* ]]; then
    echo "install-grain.sh: -repo must be OWNER/NAME, not '${repo}'" >&2
    exit 2
  fi
  [ -z "$key" ] || { echo "install-grain.sh: -key verifies a registry signature; release assets are checked by sha256 (use -image to verify a signed registry image)" >&2; exit 2; }
  command -v curl >/dev/null || { echo "install-grain.sh: needs curl to download release assets" >&2; exit 1; }
  command -v jq >/dev/null || { echo "install-grain.sh: needs jq to read the release's images.json" >&2; exit 1; }
  base="https://github.com/${repo}/releases/download/${version}"
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  curl -fsSL -o "${work}/images.json" "${base}/images.json" ||
    { echo "install-grain.sh: ${repo} release ${version} has no images.json; pull it from the registry with -image docker.io/grainai/grain" >&2; exit 1; }
  case "$(jq -r .schema "${work}/images.json")" in
    1 | 2) ;;
    *) echo "install-grain.sh: ${version}'s images.json is not schema 1 or 2" >&2; exit 1 ;;
  esac
  asset="$(jq -er .images.grain.asset "${work}/images.json")"
  sum="$(jq -er .images.grain.sha256 "${work}/images.json")"
  ref="$(jq -er .images.grain.ref "${work}/images.json")"
  id="$(jq -er .images.grain.imageId "${work}/images.json")"
  case "$asset" in */* | .* | "") echo "install-grain.sh: unsafe asset name '${asset}'" >&2; exit 1 ;; esac
  # Every field held to the contract before it reaches a URL or docker:
  # the ref must be a local grain/<image>:<tag>, the only names that are
  # loaded and never pulled (docs/release-assets.md).
  [[ "$asset" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*\.tar\.gz$ ]] || { echo "install-grain.sh: unsafe asset name '${asset}'" >&2; exit 1; }
  [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || { echo "install-grain.sh: ${version}'s images.json names no valid sha256 ('${sum}')" >&2; exit 1; }
  [[ "$id" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "install-grain.sh: ${version}'s images.json names no valid imageId ('${id}')" >&2; exit 1; }
  [[ "$ref" =~ ^grain/[a-z0-9][a-z0-9._-]*:[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$ ]] ||
    { echo "install-grain.sh: ${version}'s images.json names '${ref}', which is not a local grain/<image>:<tag> ref" >&2; exit 1; }
  # Schema 2: the asset lives on the immutable release the image names.
  holder="$(jq -r --arg v "$version" '.images.grain.release // $v' "${work}/images.json")"
  # The contract's own pattern, exactly, so a manifest cannot point the
  # download outside ${repo} (sha-x/../../<owner>/<repo>/...).
  [[ "$holder" =~ ^(sha-[0-9a-f]{12}|v[0-9]+\.[0-9]+\.[0-9]+)$ ]] || [ "$holder" = "$version" ] ||
    { echo "install-grain.sh: ${version}'s images.json names '${holder}' as the release holding its grain image" >&2; exit 1; }
  [[ "$holder" =~ ^(sha-[0-9a-f]{12}|v[0-9]+\.[0-9]+\.[0-9]+)$ ]] || { echo "install-grain.sh: ${version} names '${holder}' as the release holding ${asset}" >&2; exit 1; }
  base="https://github.com/${repo}/releases/download/${holder}"
  curl -fsSL -o "${work}/${asset}" "${base}/${asset}"
  curl -fsSL -o "${work}/${asset}.sha256" "${base}/${asset}.sha256"
  if [ "$(cat "${work}/${asset}.sha256")" != "${sum}  ${asset}" ] || ! (cd "$work" && sha256sum --quiet -c "${asset}.sha256"); then
    echo "install-grain.sh: ${asset} does not match the sha256 ${version}'s images.json names (${sum})" >&2
    exit 1
  fi
  docker load -q -i "${work}/${asset}" >/dev/null
  loaded="$(docker image inspect --format '{{.Id}}' "$ref")"
  [ "$loaded" = "$id" ] || { echo "install-grain.sh: ${ref} is ${loaded} after loading, not ${id}" >&2; exit 1; }
  pinned="$ref"
  echo "image: ${ref} (${id}, ${asset} sha256 ${sum})"
  echo "signature: NOT verified (release assets are checked against images.json's sha256, not signed; use -image and -key to verify a signed registry image)" >&2
else
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
fi

container="$(docker create "$pinned")"
trap 'docker rm "$container" >/dev/null 2>&1 || true; rm -rf "${work:-}"' EXIT
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
