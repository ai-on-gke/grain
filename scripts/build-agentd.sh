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

# Build per-platform static grain-agentd binaries and their release manifests.
#
# Usage:
#   scripts/build-agentd.sh [--ref <git ref>] [output_dir]
#
# Defaults:
#   output_dir: bin/dist-agentd
#
# Writes static (CGO_ENABLED=0) binaries and manifests for Linux amd64 and
# arm64:
#   grain-agentd-linux-amd64
#   grain-agentd-linux-amd64.manifest.json
#   grain-agentd-linux-arm64
#   grain-agentd-linux-arm64.manifest.json
#
# Environment:
#   RELEASE                    release version stamped into pkg/version.Release
#                              (defaults to scripts/build-version.sh)
#   AGENTD_RELEASE_PUBLIC_KEY  optional public key stamped into
#                              pkg/agentd/release.ReleasePublicKey (defaults to
#                              pkg/agentd/release/agentd-release.pub)
#   AGENTD_RELEASE_SIGNING_KEY optional path to Ed25519 private key PEM to sign
#                              each manifest immediately
#   AGENTD_RELEASE_ROTATIONS   optional path to JSON array of KeyRotation hops
#   GRAIN_AGENTD_MAIN          main package built (default ./cmd/grain-agentd)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_REF=""
if [ "${1:-}" = --ref ]; then
  SRC_REF="${2:?--ref needs a git ref}"
  shift 2
fi
OUT_DIR="${1:-${ROOT_DIR}/bin/dist-agentd}"
BUILDVCS="${BUILDVCS:-auto}"
GRAIN_AGENTD_MAIN="${GRAIN_AGENTD_MAIN:-./cmd/grain-agentd}"

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

SIGN_TOOL="${TMP_DIR}/grain-agentd-sign"
(cd "${ROOT_DIR}" && CGO_ENABLED=0 go build -o "${SIGN_TOOL}" ./pkg/agentd/release/cmd/grain-agentd-sign)

LDFLAGS=""
if [ -n "${RELEASE}" ]; then
  LDFLAGS="-X github.com/ai-on-gke/grain/pkg/version.Release=${RELEASE}"
fi
if [ -n "${AGENTD_RELEASE_PUBLIC_KEY:-}" ]; then
  PUB_ONE_LINE="$("${SIGN_TOOL}" pubkey-string)"
  LDFLAGS="${LDFLAGS:+${LDFLAGS} }-X github.com/ai-on-gke/grain/pkg/agentd/release.ReleasePublicKey=${PUB_ONE_LINE}"
fi

VCS_FLAG="$("${ROOT_DIR}/scripts/buildvcs.sh" "${BUILDVCS}")"

TARGETS=(
  "linux/amd64:grain-agentd-linux-amd64"
  "linux/arm64:grain-agentd-linux-arm64"
)

echo "Building standalone grain-agentd binaries ${RELEASE:-(unversioned)}${SRC_REF:+ from ${SRC_REF}} into ${OUT_DIR}..."

MANIFEST_ARGS=()
if [ -n "${RELEASE}" ]; then
  MANIFEST_ARGS+=(-version "${RELEASE}")
fi
if [ -n "${AGENTD_RELEASE_SIGNING_KEY:-}" ]; then
  MANIFEST_ARGS+=(-signing-key "${AGENTD_RELEASE_SIGNING_KEY}")
fi
if [ -n "${AGENTD_RELEASE_ROTATIONS:-}" ]; then
  MANIFEST_ARGS+=(-rotations "${AGENTD_RELEASE_ROTATIONS}")
fi

for TARGET in "${TARGETS[@]}"; do
  PLATFORM="${TARGET%%:*}"
  BIN_NAME="${TARGET##*:}"
  GOOS="${PLATFORM%%/*}"
  GOARCH="${PLATFORM##*/}"

  echo "  - Compiling ${BIN_NAME} (${GOOS}/${GOARCH}, CGO_ENABLED=0)..."
  (cd "${SRC_DIR}" && CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
    go build -buildvcs="${VCS_FLAG}" -ldflags "${LDFLAGS}" -o "${OUT_DIR}/${BIN_NAME}" "${GRAIN_AGENTD_MAIN}")

  "${SIGN_TOOL}" manifest \
    -binary "${OUT_DIR}/${BIN_NAME}" \
    -platform "${PLATFORM}" \
    -out "${OUT_DIR}/${BIN_NAME}.manifest.json" \
    "${MANIFEST_ARGS[@]}"
done

echo "Standalone grain-agentd distribution build complete:"
ls -lh "${OUT_DIR}"
