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
#     or GHCR_PAT/GITHUB_TOKEN (when registry is ghcr.io), verifies `docker
#     login` immediately in an isolated temporary DOCKER_CONFIG so invalid
#     credentials fail fast with one clear message before any image is built or
#     pushed, and writes PACKAGE_PUBLISH_* to $GITHUB_ENV when running in
#     GitHub Actions. On Docker Hub, ~/.docker/config.json is intentionally left
#     untouched so no Docker Hub credential is active during `docker build`
#     (repository-scoped Organization Access Tokens without "Public Repo
#     Read-Only" fail upstream library/* base image pulls with HTTP 401 "access
#     token has insufficient scopes"), while scripts/release-images.sh scopes
#     its publishing login to an isolated DOCKER_CONFIG.
#
#   scripts/configure-dockerhub-auth.sh --pull-only
#     CI pull mode: logs in to Docker Hub when DOCKER_PAT is set and grants
#     Public Repo Read-Only scope (raising pull rate limits from 100 anonymous
#     pulls/6h/IPv4 to authenticated org limits), and skips cleanly when
#     DOCKER_PAT is absent (e.g. fork PR runs) or is scoped only to grainai/*
#     without Public Repo Read-Only.
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

login_tmp_dir=""
cleanup_login_tmp() {
  if [ -n "$login_tmp_dir" ]; then
    rm -rf "$login_tmp_dir" 2>/dev/null || true
    login_tmp_dir=""
  fi
}
trap cleanup_login_tmp EXIT INT TERM

# Verify credentials against $1 without mutating the runner's ~/.docker/config.json.
verify_docker_login() {
  local reg="$1" login_user="$2" login_token="$3"
  if [ -z "$login_tmp_dir" ]; then
    login_tmp_dir="$(mktemp -d)"
    chmod 700 "$login_tmp_dir"
  fi
  printf '%s' "$login_token" | DOCKER_CONFIG="$login_tmp_dir" docker login "$reg" -u "$login_user" --password-stdin
}

# Probe whether a Docker Hub credential has "Public Repo Read-Only" scope
# (ability to pull library/*). Organization Access Tokens (dckr_oat_...) scoped
# only to grainai/* pass `docker login docker.io` and can push/pull grainai/*,
# yet auth.docker.io rejects authenticated token requests for library/* with
# 401 ("access token has insufficient scopes"). Leaving such a token in
# ~/.docker/config.json breaks `docker build` of upstream base images.
dockerhub_token_can_pull_library() {
  local check_user="$1" check_token="$2" code
  command -v curl >/dev/null 2>&1 || return 1
  code="$(curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 5 --max-time 10 \
    -u "${check_user}:${check_token}" \
    "${DOCKERHUB_AUTH_URL:-https://auth.docker.io/token}?scope=repository%3Alibrary%2Fdebian%3Apull&service=registry.docker.io" 2>/dev/null || true)"
  [ "$code" = "200" ]
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
  if ! verify_docker_login docker.io "$dockerhub_user" "$token"; then
    echo "::error::Docker Hub login failed for user '${dockerhub_user}' (check DOCKER_PAT and DOCKERHUB_USERNAME)." >&2
    exit 1
  fi
  if dockerhub_token_can_pull_library "$dockerhub_user" "$token"; then
    if ! printf '%s' "$token" | docker login docker.io -u "$dockerhub_user" --password-stdin >/dev/null; then
      echo "::error::Docker Hub login failed for user '${dockerhub_user}' (check DOCKER_PAT and DOCKERHUB_USERNAME)." >&2
      exit 1
    fi
  else
    echo "configure-dockerhub-auth.sh: DOCKER_PAT for '${dockerhub_user}' lacks Public Repo Read-Only scope (library/debian:pull not permitted); keeping ~/.docker/config.json unauthenticated so upstream base image pulls succeed"
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
    if ! verify_docker_login "$registry" "$user" "$token"; then
      echo "::error::Docker Hub login failed for '${user}' on ${registry} (verify the DOCKER_PAT secret and DOCKERHUB_USERNAME repository variable). Stopping before building or pushing any release images." >&2
      exit 1
    fi
    echo "configure-dockerhub-auth.sh: verified Docker Hub login for '${user}'; keeping ~/.docker/config.json unauthenticated for upstream base image builds while exporting PACKAGE_PUBLISH_* for ${repo}/*"
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
