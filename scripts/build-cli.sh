#!/usr/bin/env bash
# Build standalone Grain CLI binaries for macOS and Linux distribution.
#
# Usage:
#   scripts/build-cli.sh [output_dir]
#
# Defaults:
#   output_dir: bin/dist
#
# GRAIN_MAIN is the main package built: ./cmd/grain, core grain, unless
# it says otherwise (see the Makefile's MAIN_grain).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-${ROOT_DIR}/bin/dist}"
BUILDVCS="${BUILDVCS:-auto}"
GRAIN_MAIN="${GRAIN_MAIN:-./cmd/grain}"

mkdir -p "${OUT_DIR}"

VCS_FLAG="$("${ROOT_DIR}/scripts/buildvcs.sh" "${BUILDVCS}")"

TARGETS=(
  "darwin/arm64:grain-darwin-arm64"
  "darwin/amd64:grain-darwin-amd64"
  "linux/arm64:grain-linux-arm64"
  "linux/amd64:grain-linux-amd64"
)

echo "Building standalone Grain CLI binaries into ${OUT_DIR}..."

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

for TARGET in "${TARGETS[@]}"; do
  PLATFORM="${TARGET%%:*}"
  BIN_NAME="${TARGET##*:}"
  GOOS="${PLATFORM%%/*}"
  GOARCH="${PLATFORM##*/}"

  echo "  - Compiling ${BIN_NAME} (${GOOS}/${GOARCH})..."
  CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
    go build -buildvcs="${VCS_FLAG}" -o "${OUT_DIR}/${BIN_NAME}" "${ROOT_DIR}/${GRAIN_MAIN#./}"

  # Package into a tarball containing standard binary name "grain"
  ARCHIVE_NAME="${BIN_NAME}.tar.gz"
  STAGE_DIR="${TMP_DIR}/${BIN_NAME}"
  mkdir -p "${STAGE_DIR}"
  cp "${OUT_DIR}/${BIN_NAME}" "${STAGE_DIR}/grain"
  tar -czf "${OUT_DIR}/${ARCHIVE_NAME}" -C "${STAGE_DIR}" grain
done

echo "Generating SHA256 checksums..."
(
  cd "${OUT_DIR}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum grain-* > checksums.txt
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 grain-* > checksums.txt
  fi
)

echo "Standalone CLI distribution build complete:"
ls -lh "${OUT_DIR}"
