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

# Scan Grain's release images for known vulnerabilities and gate on them
# (docs/image-vulnerability-scanning.md).
#
# Trivy scans each image in image mode with both of its package scanners
# on -- the image's OS packages (the Debian base and everything apt put
# there) and its language packages (the modules of every Go binary, npm
# trees, and so on) -- which is the same ground Artifact Registry's
# Container Analysis covers. cmd/image-vulns then applies the policy:
# a fixable CRITICAL or HIGH finding that no unexpired entry in
# .grain/baselines/image-vulns-allowlist.yaml covers fails; everything
# else is reported.
#
# Usage:
#   scan-images.sh [options] [<image>=<ref> ...]
#
# Which images, in order of precedence:
#   <image>=<ref> ...   exactly these (image is the leaf name an allowlist
#                       entry names: grain, sandbox, sandbox-k8s, hub)
#   --state FILE        the images scripts/release-images.sh build recorded
#                       in FILE (its $RELEASE_IMAGES_STATE)
#   --tag TAG           the published images at TAG (docker.io/grainai/<leaf>:TAG,
#                       or wherever RELEASE_IMAGE_REPO and
#                       PACKAGE_PUBLISH_REGISTRY point)
#   (none)              $RELEASE_IMAGES_STATE if that file exists, else --tag main
#
# Options:
#   --out DIR           where reports go (default: image-vulns-report)
#   --report-only       never fail; for the weekly report
#   --previous FILE     an earlier image-vulns.json; new findings are marked
#   --wait SECONDS      wait up to this long for a published image to appear
#                       (an rc's images are published beside its qualification)
#   --title TEXT        the summary's heading
#
# Writes, in --out: <image>.trivy.json (Trivy's full report),
# <image>.sarif (for code scanning), <image>.txt (Trivy's table),
# image-vulns.json (the gate's evaluated findings) and summary.md, which
# is also appended to $GITHUB_STEP_SUMMARY when that is set.
#
# Environment:
#   TRIVY               a trivy binary to use instead of the pinned one
#   TRIVY_CACHE_DIR     Trivy's DB and cache (default ~/.cache/trivy; CI caches it)
#   IMAGE_VULNS_ALLOWLIST  the allowlist (default .grain/baselines/image-vulns-allowlist.yaml)
#   IMAGE_VULNS_SARIF_CATEGORY  code scanning's category prefix (default image-vulns;
#                       each image's SARIF is <prefix>/<image>/)
set -euo pipefail

scripts_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$scripts_dir/.." && pwd)"

# The scanner, pinned by version and by the sha256 of each release
# archive (github.com/aquasecurity/trivy/releases, trivy_<v>_checksums.txt).
# Never a moving tag, and never the trivy-action: a scanner is the last
# thing a release should take on trust. Bump all of these together.
TRIVY_VERSION="${TRIVY_VERSION:-0.76.2}"
trivy_sha256() {
  case "$1" in
    Linux-64bit) echo bc0fc726e017b8927a62b2d3531d7a84ee01769daf5c99ed866dced54089fdbf ;;
    Linux-ARM64) echo ab9d51f86e6a5fb936f605faff6b593ac51bc378fff4d3e8b9d58358b9c367c3 ;;
    macOS-64bit) echo e08bb9ea449ecf66c6bfb03cbb8697c503ff33d35fc585c9747e3eaeb7ed4c7b ;;
    macOS-ARM64) echo ae7ea358326960406b703c77b2ac490190797d80b306b5b50c2a27221da0a5b8 ;;
    *) return 1 ;;
  esac
}

out="image-vulns-report"
state=""
tag=""
report_only=false
previous=""
wait_secs=0
title="Image vulnerability scan"
pairs=()
while [ $# -gt 0 ]; do
  case "$1" in
    --out) out="$2"; shift 2 ;;
    --state) state="$2"; shift 2 ;;
    --tag) tag="$2"; shift 2 ;;
    --report-only) report_only=true; shift ;;
    --previous) previous="$2"; shift 2 ;;
    --wait) wait_secs="$2"; shift 2 ;;
    --title) title="$2"; shift 2 ;;
    -h | --help) sed -n '17,58p' "$0"; exit 0 ;;
    *=*) pairs+=("$1"); shift ;;
    *) echo "scan-images.sh: unknown argument $1" >&2; exit 64 ;;
  esac
done

ensure_trivy() {
  if [ -n "${TRIVY:-}" ]; then
    trivy="$TRIVY"
    return
  fi
  local os arch platform sum dir tmp
  os="$(uname -s)"
  arch="$(uname -m)"
  case "$os/$arch" in
    Linux/x86_64) platform=Linux-64bit ;;
    Linux/aarch64 | Linux/arm64) platform=Linux-ARM64 ;;
    Darwin/x86_64) platform=macOS-64bit ;;
    Darwin/arm64) platform=macOS-ARM64 ;;
    *) echo "scan-images.sh: no pinned trivy for $os/$arch; set TRIVY" >&2; exit 1 ;;
  esac
  sum="$(trivy_sha256 "$platform")"
  dir="${XDG_CACHE_HOME:-$HOME/.cache}/grain/trivy-$TRIVY_VERSION"
  trivy="$dir/trivy"
  if [ -x "$trivy" ] && [ "$(cat "$dir/.sha256" 2>/dev/null)" = "$sum" ]; then
    return
  fi
  mkdir -p "$dir"
  tmp="$(mktemp)"
  curl -fsSL --retry 5 --retry-all-errors -o "$tmp" \
    "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_${platform}.tar.gz"
  if ! echo "$sum  $tmp" | sha256sum -c - >/dev/null 2>&1 &&
     ! echo "$sum  $tmp" | shasum -a 256 -c - >/dev/null 2>&1; then
    rm -f "$tmp"
    echo "scan-images.sh: trivy ${TRIVY_VERSION} ${platform} does not match its pinned sha256 $sum; refusing to run it" >&2
    exit 1
  fi
  tar -xzf "$tmp" -C "$dir" trivy
  rm -f "$tmp"
  echo "$sum" > "$dir/.sha256"
}

# The images, as name=ref pairs.
if [ "${#pairs[@]}" -eq 0 ]; then
  if [ -z "$state" ] && [ -z "$tag" ]; then
    if [ -n "${RELEASE_IMAGES_STATE:-}" ] && [ -f "$RELEASE_IMAGES_STATE" ]; then
      state="$RELEASE_IMAGES_STATE"
    else
      tag=main
    fi
  fi
  if [ -n "$state" ]; then
    # shellcheck source=/dev/null
    . "$state"
    # registry is hub under its pre-rename name, the same image, so it is
    # scanned once, as hub.
    pairs=(
      "grain=${RELEASE_IMAGE_GRAIN:?$state names no RELEASE_IMAGE_GRAIN}"
      "sandbox=${RELEASE_IMAGE_GUEST:?$state names no RELEASE_IMAGE_GUEST}"
      "sandbox-k8s=${RELEASE_IMAGE_K8S_SANDBOX:?$state names no RELEASE_IMAGE_K8S_SANDBOX}"
      "hub=${RELEASE_IMAGE_HUB:?$state names no RELEASE_IMAGE_HUB}"
    )
  else
    # shellcheck source=scripts/release-image-repos.sh
    . "$scripts_dir/release-image-repos.sh"
    for role in daemon sandbox sandbox-k8s hub; do
      pairs+=("$(release_image_leaf_for "$role")=$(release_image_repo_for "$role"):$tag")
    done
  fi
fi

ensure_trivy
mkdir -p "$out"
# Absolute, because the gate below runs from the repository root.
out="$(cd "$out" && pwd)"
if [ -n "$previous" ] && [ -f "$previous" ]; then
  previous="$(cd "$(dirname "$previous")" && pwd)/$(basename "$previous")"
fi
export TRIVY_CACHE_DIR="${TRIVY_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/trivy}"
allowlist="${IMAGE_VULNS_ALLOWLIST:-$repo_root/.grain/baselines/image-vulns-allowlist.yaml}"

# Fetch the DB once, up front, so a failure to reach it says so rather
# than surfacing as the first image's scan failing.
"$trivy" image --download-db-only --quiet

reports=()
for pair in "${pairs[@]}"; do
  name="${pair%%=*}"
  ref="${pair#*=}"
  if [ "$wait_secs" -gt 0 ]; then
    deadline=$(( $(date +%s) + wait_secs ))
    until docker image inspect "$ref" >/dev/null 2>&1 || docker manifest inspect "$ref" >/dev/null 2>&1; do
      if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "scan-images.sh: $ref did not appear within ${wait_secs}s" >&2
        exit 1
      fi
      echo "scan-images.sh: waiting for $ref to be published"
      sleep 60
    done
  fi
  echo "scan-images.sh: scanning $name ($ref)"
  # One scan, every severity, nothing ignored: the policy is
  # cmd/image-vulns's, and the reports should show everything.
  "$trivy" image --quiet --skip-db-update \
    --scanners vuln --pkg-types os,library \
    --timeout 30m \
    --format json --output "$out/$name.trivy.json" "$ref"
  # Code scanning gets the CRITICAL and HIGH findings, which are what
  # the gate is about; the JSON and table reports carry every severity.
  # Each image's SARIF names its own category, so one upload of the
  # directory files four images' alerts apart rather than refusing two
  # runs of the same tool under one category.
  "$trivy" convert --quiet --format sarif --severity CRITICAL,HIGH --output "$out/$name.sarif.tmp" "$out/$name.trivy.json"
  jq --arg id "${IMAGE_VULNS_SARIF_CATEGORY:-image-vulns}/$name/" \
    '.runs |= map(.automationDetails = {id: $id})' "$out/$name.sarif.tmp" > "$out/$name.sarif"
  rm -f "$out/$name.sarif.tmp"
  "$trivy" convert --quiet --format table --output "$out/$name.txt" "$out/$name.trivy.json"
  reports+=("$name=$out/$name.trivy.json")
done

gate_args=(-allowlist "$allowlist" -json "$out/image-vulns.json" -markdown "$out/summary.md" -title "$title")
[ "$report_only" = true ] && gate_args+=(-report-only)
[ -n "$previous" ] && gate_args+=(-previous "$previous")
status=0
(cd "$repo_root" && go run ./cmd/image-vulns "${gate_args[@]}" "${reports[@]}") || status=$?
cat "$out/summary.md" 2>/dev/null || true
if [ -n "${GITHUB_STEP_SUMMARY:-}" ] && [ -f "$out/summary.md" ]; then
  cat "$out/summary.md" >> "$GITHUB_STEP_SUMMARY"
fi
exit "$status"
