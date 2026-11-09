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

# Single source of truth for Grain's published open-source OCI image
# registry, namespace, leaf repository names, and legacy GHCR <-> Docker Hub
# aliases. Keep in sync with pkg/installtrack/installtrack.go.
# shellcheck disable=SC2034

RELEASE_IMAGE_REGISTRY_DEFAULT="docker.io"
RELEASE_IMAGE_REPO_DEFAULT="grainai"
DOCKERHUB_USERNAME_DEFAULT="grainai"

# Single table of published open-source Grain OCI repositories:
#   <role>|<dockerhub-leaf>|<primary-legacy-ghcr-leaf>|<extra-legacy-ghcr-leafs-csv>
# Adding a new published image later (for example, `registry|registry||` for
# the reserved `docker.io/grainai/registry` image once it exists) is a one-line
# addition to GRAIN_OSS_IMAGE_TABLE.
GRAIN_OSS_IMAGE_TABLE='
daemon|grain|grain|
sandbox|sandbox|guest|granule-sandbox
sandbox-base|sandbox-base|guest-base|
sandbox-k8s|sandbox-k8s|k8s-sandbox|
sandbox-k8s-base|sandbox-k8s-base|k8s-sandbox-base|
hub|hub||
registry|registry||
'

GRAIN_OSS_DAEMON_IMAGE_NAME="grain"
GRAIN_OSS_SANDBOX_IMAGE_NAME="sandbox"
GRAIN_OSS_SANDBOX_BASE_IMAGE_NAME="sandbox-base"
GRAIN_OSS_K8S_SANDBOX_IMAGE_NAME="sandbox-k8s"
GRAIN_OSS_K8S_SANDBOX_BASE_IMAGE_NAME="sandbox-k8s-base"
GRAIN_OSS_HUB_IMAGE_NAME="hub"
# The hub image's pre-rename name (the Grain Hub was the Grain Registry),
# published beside hub with the same tags for one release.
GRAIN_OSS_REGISTRY_IMAGE_NAME="registry"

GRAIN_OSS_IMAGE_REPO_DEFAULT="${RELEASE_IMAGE_REGISTRY_DEFAULT}/${RELEASE_IMAGE_REPO_DEFAULT}/${GRAIN_OSS_DAEMON_IMAGE_NAME}"
GRAIN_LEGACY_GHCR_PREFIX="ghcr.io/bwsalmon/grain"

# Derive comma-separated package list from GRAIN_OSS_IMAGE_TABLE.
_grain_published_packages=""
while IFS='|' read -r _role _oss_leaf _legacy_leaf _extra_legacy; do
  [ -n "${_oss_leaf:-}" ] || continue
  if [ -z "$_grain_published_packages" ]; then
    _grain_published_packages="$_oss_leaf"
  else
    _grain_published_packages="${_grain_published_packages},${_oss_leaf}"
  fi
done <<EOF
$GRAIN_OSS_IMAGE_TABLE
EOF
GRAIN_PUBLISHED_IMAGE_PACKAGES="$_grain_published_packages"
unset _grain_published_packages _role _oss_leaf _legacy_leaf _extra_legacy

# release_image_leaf_for resolves a logical role (`daemon`, `sandbox`, ...) or
# already-resolved leaf name (`grain`, `sandbox`, ...) via GRAIN_OSS_IMAGE_TABLE.
release_image_leaf_for() {
  local key="${1:-$GRAIN_OSS_DAEMON_IMAGE_NAME}"
  local role oss_leaf legacy_leaf extra_legacy
  while IFS='|' read -r role oss_leaf legacy_leaf extra_legacy; do
    [ -n "${oss_leaf:-}" ] || continue
    if [ "$key" = "$role" ] || [ "$key" = "$oss_leaf" ]; then
      printf '%s\n' "$oss_leaf"
      return 0
    fi
  done <<EOF
$GRAIN_OSS_IMAGE_TABLE
EOF
  printf '%s\n' "$key"
}

# release_image_repo_for prints the target `<registry>/<namespace>/<name>`
# repository for a logical role or image leaf name (default `grain`), using
# PACKAGE_PUBLISH_REGISTRY and RELEASE_IMAGE_REPO when set.
release_image_repo_for() {
  local name
  name="$(release_image_leaf_for "${1:-$GRAIN_OSS_DAEMON_IMAGE_NAME}")"
  local reg="${PACKAGE_PUBLISH_REGISTRY:-$RELEASE_IMAGE_REGISTRY_DEFAULT}"
  local ns="${RELEASE_IMAGE_REPO:-$RELEASE_IMAGE_REPO_DEFAULT}"
  printf '%s/%s/%s\n' "${reg,,}" "${ns,,}" "$name"
}

# canonical_oss_image_repo maps a legacy GHCR Grain image repository to its
# Docker Hub replacement using GRAIN_OSS_IMAGE_TABLE, leaving any other
# repository unchanged.
canonical_oss_image_repo() {
  local repo="${1:-}"
  local lowered="${repo,,}"
  local role oss_leaf legacy_leaf extra_legacy candidate
  while IFS='|' read -r role oss_leaf legacy_leaf extra_legacy; do
    [ -n "${oss_leaf:-}" ] || continue
    for candidate in $legacy_leaf $(printf '%s' "${extra_legacy:-}" | tr ',' ' '); do
      [ -n "$candidate" ] || continue
      if [ "$lowered" = "ghcr.io/bwsalmon/grain/${candidate}" ] || [ "$lowered" = "ghcr.io/ai-on-gke/grain/${candidate}" ]; then
        printf '%s/%s/%s\n' "$RELEASE_IMAGE_REGISTRY_DEFAULT" "$RELEASE_IMAGE_REPO_DEFAULT" "$oss_leaf"
        return 0
      fi
    done
  done <<EOF
$GRAIN_OSS_IMAGE_TABLE
EOF
  printf '%s\n' "$repo"
}

# legacy_ghcr_image_repo maps a canonical Docker Hub Grain image repository to
# its historical GHCR counterpart (empty if none), used only as a read-only
# bootstrap fallback before the first release populates a tag on Docker Hub.
legacy_ghcr_image_repo() {
  local repo="${1:-}"
  local lowered="${repo,,}"
  local role oss_leaf legacy_leaf extra_legacy ns_leaf
  while IFS='|' read -r role oss_leaf legacy_leaf extra_legacy; do
    if [ -z "${oss_leaf:-}" ] || [ -z "${legacy_leaf:-}" ]; then
      continue
    fi
    ns_leaf="${RELEASE_IMAGE_REPO_DEFAULT}/${oss_leaf}"
    case "$lowered" in
      "${RELEASE_IMAGE_REGISTRY_DEFAULT}/${ns_leaf}" | "index.docker.io/${ns_leaf}" | "${ns_leaf}")
        printf '%s/%s\n' "$GRAIN_LEGACY_GHCR_PREFIX" "$legacy_leaf"
        return 0
        ;;
    esac
  done <<EOF
$GRAIN_OSS_IMAGE_TABLE
EOF
}

# legacy_ghcr_image_for maps a tag-qualified Docker Hub Grain image reference
# (`docker.io/grainai/<name>:<tag>`) to its legacy GHCR tag reference, or
# prints nothing if it is digest-pinned or not an official Grain repository.
legacy_ghcr_image_for() {
  local ref="${1:-}" repo tag legacy_repo
  case "$ref" in
    *@* | "") return 0 ;;
  esac
  repo="$ref"
  tag=""
  case "${ref##*/}" in
    *:*)
      repo="${ref%:*}"
      tag=":${ref##*:}"
      ;;
  esac
  legacy_repo="$(legacy_ghcr_image_repo "$repo")"
  [ -n "$legacy_repo" ] || return 0
  printf '%s%s\n' "$legacy_repo" "$tag"
}
