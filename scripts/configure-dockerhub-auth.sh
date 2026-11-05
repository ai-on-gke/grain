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

# Configure registry authentication for Docker Hub (or a custom override
# registry via PACKAGE_PUBLISH_REGISTRY) in GitHub Actions workflows.
#
# Modes:
#   scripts/configure-dockerhub-auth.sh
#     Publish mode (default): requires DOCKER_PAT (when registry is docker.io)
#     or GHCR_PAT/GITHUB_TOKEN (when registry is ghcr.io), runs `docker login`
#     immediately so invalid credentials fail fast with one clear message before
#     any image is built or pushed, and writes PACKAGE_PUBLISH_* to $GITHUB_ENV
#     when running in GitHub Actions.
#
#   scripts/configure-dockerhub-auth.sh --pull-only
#     CI pull mode: logs in to Docker Hub when DOCKER_PAT is set (raising pull
#     rate limits from 100 anonymous pulls/6h/IPv4 to authenticated org limits),
#     and skips cleanly when DOCKER_PAT is absent (e.g. fork PR runs).
#
#   scripts/configure-dockerhub-auth.sh --dry-run
#     Dry-run publish mode: exports PACKAGE_PUBLISH_REGISTRY and
#     RELEASE_IMAGE_REPO without requiring credentials or running `docker login`.

set -euo pipefail

case "${BASH_SOURCE[0]}" in
  */*) scripts_dir="${BASH_SOURCE[0]%/*}" ;;
  *) scripts_dir=. ;;
esac
# shellcheck source=scripts/release-image-repos.sh
. "$scripts_dir/release-image-repos.sh"

mode="publish"
while [ $# -gt 0 ]; do
  case "$1" in
    --pull-only)
      mode="pull-only"
      shift
      ;;
    --dry-run)
      mode="dry-run"
      shift
      ;;
    *)
      echo "configure-dockerhub-auth.sh: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

registry="${PUBLISH_REGISTRY:-${PACKAGE_PUBLISH_REGISTRY:-$RELEASE_IMAGE_REGISTRY_DEFAULT}}"
registry="${registry,,}"
repo="${RELEASE_IMAGE_REPO:-$RELEASE_IMAGE_REPO_DEFAULT}"
dockerhub_user="${DOCKERHUB_USERNAME:-$DOCKERHUB_USERNAME_DEFAULT}"

write_github_env() {
  if [ -n "${GITHUB_ENV:-}" ]; then
    "$@" >> "$GITHUB_ENV"
  fi
}

if [ "$mode" = "dry-run" ]; then
  write_github_env printf '%s\n' \
    "PACKAGE_PUBLISH_REGISTRY=${registry}" \
    "RELEASE_IMAGE_REPO=${repo}"
  exit 0
fi

if [ "$mode" = "pull-only" ]; then
  token="${DOCKER_PAT:-}"
  if [ -z "$token" ]; then
    echo "configure-dockerhub-auth.sh: DOCKER_PAT is not set; using unauthenticated Docker Hub pulls"
    exit 0
  fi
  if ! printf '%s' "$token" | docker login docker.io -u "$dockerhub_user" --password-stdin; then
    echo "::error::Docker Hub login failed for user '${dockerhub_user}' (check DOCKER_PAT and DOCKERHUB_USERNAME)." >&2
    exit 1
  fi
  exit 0
fi

case "$registry" in
  docker.io | index.docker.io)
    token="${DOCKER_PAT:-${PACKAGE_PUBLISH_TOKEN:-}}"
    user="${PACKAGE_PUBLISH_USERNAME:-$dockerhub_user}"
    if [ -z "$token" ]; then
      echo "::error::DOCKER_PAT secret is required to publish images to ${registry}/${repo}/* (log in as '${user}', configurable via DOCKERHUB_USERNAME). Stopping before building or pushing any release images." >&2
      exit 1
    fi
    if ! printf '%s' "$token" | docker login "$registry" -u "$user" --password-stdin; then
      echo "::error::Docker Hub login failed for '${user}' on ${registry} (verify the DOCKER_PAT secret and DOCKERHUB_USERNAME repository variable). Stopping before building or pushing any release images." >&2
      exit 1
    fi
    write_github_env printf '%s\n' \
      "PACKAGE_PUBLISH_REGISTRY=${registry}" \
      "RELEASE_IMAGE_REPO=${repo}" \
      "PACKAGE_PUBLISH_USERNAME=${user}" \
      "PACKAGE_PUBLISH_TOKEN=${token}"
    ;;
  ghcr.io)
    token="${GHCR_PAT:-${PACKAGE_PUBLISH_TOKEN:-${GITHUB_TOKEN:-}}}"
    user="${PACKAGE_PUBLISH_USERNAME:-${GITHUB_ACTOR:-grain}}"
    if [ -z "$token" ]; then
      echo "::error::A registry token (GHCR_TOKEN or PACKAGE_PUBLISH_TOKEN) is required to publish images to ghcr.io/${repo}/*. Stopping before building or pushing any release images." >&2
      exit 1
    fi
    if ! printf '%s' "$token" | docker login ghcr.io -u "$user" --password-stdin; then
      echo "::error::GHCR login failed for '${user}' on ghcr.io. Stopping before building or pushing any release images." >&2
      exit 1
    fi
    write_github_env printf '%s\n' \
      "PACKAGE_PUBLISH_REGISTRY=ghcr.io" \
      "RELEASE_IMAGE_REPO=${repo}" \
      "PACKAGE_PUBLISH_USERNAME=${user}" \
      "PACKAGE_PUBLISH_TOKEN=${token}"
    ;;
  *)
    token="${PACKAGE_PUBLISH_TOKEN:-${DOCKER_PAT:-}}"
    user="${PACKAGE_PUBLISH_USERNAME:-$dockerhub_user}"
    if [ -n "$token" ]; then
      if ! printf '%s' "$token" | docker login "$registry" -u "$user" --password-stdin; then
        echo "::error::Registry login failed for '${user}' on ${registry}. Stopping before building or pushing any release images." >&2
        exit 1
      fi
      write_github_env printf '%s\n' \
        "PACKAGE_PUBLISH_REGISTRY=${registry}" \
        "RELEASE_IMAGE_REPO=${repo}" \
        "PACKAGE_PUBLISH_USERNAME=${user}" \
        "PACKAGE_PUBLISH_TOKEN=${token}"
    else
      write_github_env printf '%s\n' \
        "PACKAGE_PUBLISH_REGISTRY=${registry}" \
        "RELEASE_IMAGE_REPO=${repo}"
    fi
    ;;
esac
