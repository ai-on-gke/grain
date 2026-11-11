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

# Build standalone Grain CLI binaries for macOS and Linux distribution.
#
# Usage:
#   scripts/build-cli.sh [--ref <git ref>] [output_dir]
#
# Defaults:
#   output_dir: bin/dist
#
# It writes grain-<os>-<arch> and grain-<os>-<arch>.tar.gz (holding one
# file, grain) for linux and darwin on amd64 and arm64, and checksums.txt
# naming the four archives in sha256sum's format (`<hex>  <archive>`).
# Those are the names GET /cli/install.sh (pkg/ui/cli_install.sh) fetches
# off a release, and the files `release-assets.sh cli-publish` uploads.
#
# Every binary is stamped with the version a build of the source carries,
# as `make build` stamps the daemon: $RELEASE when it is set (empty
# included, which leaves it unversioned), otherwise what
# scripts/build-version.sh works out. A release build passes
# RELEASE=vX.Y.Z (or the vX.Y.Z-rc.N tag itself), the version its daemon
# reports, so the install script a daemon serves resolves to a release
# whose CLI is the daemon's own.
#
# --ref builds that git ref (a release tag, say) from a detached worktree
# of this checkout instead of the checkout itself, with this script.
#
# GRAIN_MAIN is the main package built: ./cmd/grain, core grain, unless
# it says otherwise (see the Makefile's MAIN_grain).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_REF=""
if [ "${1:-}" = --ref ]; then
  SRC_REF="${2:?--ref needs a git ref}"
  shift 2
fi
OUT_DIR="${1:-${ROOT_DIR}/bin/dist}"
BUILDVCS="${BUILDVCS:-auto}"
GRAIN_MAIN="${GRAIN_MAIN:-./cmd/grain}"

mkdir -p "${OUT_DIR}"
OUT_DIR="$(cd "${OUT_DIR}" && pwd)"

TMP_DIR="$(mktemp -d)"
SRC_DIR="${ROOT_DIR}"
cleanup() {
  if [ "${SRC_DIR}" != "${ROOT_DIR}" ]; then
    git -C "${ROOT_DIR}" worktree remove --force "${SRC_DIR}" >/dev/null 2>&1 || true
  fi
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

if [ -n "${SRC_REF}" ]; then
  SRC_DIR="${TMP_DIR}/src"
  git -C "${ROOT_DIR}" worktree add --detach --quiet "${SRC_DIR}" "${SRC_REF}^{commit}"
fi

if [ "${RELEASE+set}" != set ]; then
  RELEASE="$(cd "${SRC_DIR}" && bash "${ROOT_DIR}/scripts/build-version.sh" 2>/dev/null)" || RELEASE=""
fi
LDFLAGS=""
if [ -n "${RELEASE}" ]; then
  LDFLAGS="-X github.com/ai-on-gke/grain/pkg/version.Release=${RELEASE}"
fi

VCS_FLAG="$("${ROOT_DIR}/scripts/buildvcs.sh" "${BUILDVCS}")"

TARGETS=(
  "darwin/arm64:grain-darwin-arm64"
  "darwin/amd64:grain-darwin-amd64"
  "linux/arm64:grain-linux-arm64"
  "linux/amd64:grain-linux-amd64"
)

echo "Building standalone Grain CLI binaries ${RELEASE:-(unversioned)}${SRC_REF:+ from ${SRC_REF}} into ${OUT_DIR}..."

ARCHIVES=()
for TARGET in "${TARGETS[@]}"; do
  PLATFORM="${TARGET%%:*}"
  BIN_NAME="${TARGET##*:}"
  GOOS="${PLATFORM%%/*}"
  GOARCH="${PLATFORM##*/}"

  echo "  - Compiling ${BIN_NAME} (${GOOS}/${GOARCH})..."
  (cd "${SRC_DIR}" && CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
    go build -buildvcs="${VCS_FLAG}" -ldflags "${LDFLAGS}" -o "${OUT_DIR}/${BIN_NAME}" "${GRAIN_MAIN}")

  # Package into a tarball containing standard binary name "grain"
  ARCHIVE_NAME="${BIN_NAME}.tar.gz"
  STAGE_DIR="${TMP_DIR}/${BIN_NAME}"
  mkdir -p "${STAGE_DIR}"
  cp "${OUT_DIR}/${BIN_NAME}" "${STAGE_DIR}/grain"
  tar -czf "${OUT_DIR}/${ARCHIVE_NAME}" -C "${STAGE_DIR}" grain
  ARCHIVES+=("${ARCHIVE_NAME}")
done

# The archives alone: they are what a release publishes and the install
# script checks. shasum -a 256 writes the same `<hex>  <name>` lines.
echo "Generating SHA256 checksums..."
(
  cd "${OUT_DIR}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${ARCHIVES[@]}" > checksums.txt
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "${ARCHIVES[@]}" > checksums.txt
  else
    echo "build-cli.sh: needs sha256sum or shasum to write checksums.txt" >&2
    exit 1
  fi
)

echo "Standalone CLI distribution build complete:"
ls -lh "${OUT_DIR}"
