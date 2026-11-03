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

# Prepares kind, the pinned Calico manifest, and the container image archives
# for the `real kubernetes` CI job on GitHub-hosted ubuntu-latest runners.
#
# Hashing this script in actions/cache keys the cached archives on every pinned
# version and image digest below. On a cache hit, zero container registry or
# GitHub raw fetches occur, and the workload images stream straight from the
# archive into kind's containerd without unpacking into the host Docker store.

set -euo pipefail

CACHE_DIR="${GRAIN_K8S_CACHE_DIR:-$HOME/.cache/grain-k8s}"
WORKLOAD_ARCHIVE="${GRAIN_K8S_IMAGE_ARCHIVE:-/var/tmp/k8s-workload-images.tar}"
CALICO_OUT="${GRAIN_CALICO_MANIFEST:-/var/tmp/calico.yaml}"

KIND_VERSION="v0.32.0"
KIND_SHA256_AMD64="50030de23cf40a18505f20426f6a8506bedf13c6e509244bd1fa9463721b0f54"

KIND_NODE_VERSION="v1.36.1"
KIND_NODE_TAG="kindest/node:${KIND_NODE_VERSION}"
KIND_NODE_PIN="kindest/node:${KIND_NODE_VERSION}@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5"

CALICO_VERSION="v3.33.0"
CALICO_MANIFEST_URL="https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml"
CALICO_MANIFEST_SHA256="2de8f47595fb9c41b3f47d7b767a1f8e72ecf84057af834738ff12689a234da5"

WORKLOAD_PINS=(
  "quay.io/calico/calico:v3.33.0@sha256:4ebc20730127bc9e8bf1db5f5a2d8b58cf37fe0e891b3c7b5471992221d213e5|quay.io/calico/calico:v3.33.0"
  "quay.io/calico/node:v3.33.0@sha256:565d8db3200e443955557f8f28ff44ab8a4a456754bc74a19c4f2f3444af9b1c|quay.io/calico/node:v3.33.0"
  "quay.io/calico/third-party-cni-plugins:v3.33.0@sha256:0f32f48279484c0d6f0e0d0e767b2d0e1a6c6ca34cb3421a61b989d992f3df41|quay.io/calico/third-party-cni-plugins:v3.33.0"
  "buildpack-deps:bookworm-curl@sha256:f5c5d07ea24ee91272739fe451c280aa4b7fa5120b9278815db5fcd713d83772|buildpack-deps:bookworm-curl"
  "nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10|nginx:1.27-alpine"
)

mkdir -p "$CACHE_DIR" "$(dirname "$WORKLOAD_ARCHIVE")" "$(dirname "$CALICO_OUT")"

if ! command -v zstd >/dev/null 2>&1; then
  sudo rm -f /etc/apt/sources.list.d/google-chrome.list /etc/apt/sources.list.d/microsoft-prod.list
  sudo apt-get update -qq
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends zstd
fi

sudo sysctl -w fs.inotify.max_user_watches=524288 >/dev/null 2>&1 || true
sudo sysctl -w fs.inotify.max_user_instances=512 >/dev/null 2>&1 || true

# 1. Ensure pinned kind binary (amd64).
cached_kind="${CACHE_DIR}/kind-${KIND_VERSION}-linux-amd64"
current_kind_ver=""
if command -v kind >/dev/null 2>&1; then
  current_kind_ver="$(kind version | awk '{print $2}')"
fi
if [ "$current_kind_ver" != "$KIND_VERSION" ]; then
  if [ ! -f "$cached_kind" ]; then
    echo "Downloading kind ${KIND_VERSION}..."
    curl -4 -fsSL --retry 3 --max-time 120 \
      "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64" \
      -o "${cached_kind}.tmp"
    echo "${KIND_SHA256_AMD64}  ${cached_kind}.tmp" | sha256sum -c -
    chmod 0755 "${cached_kind}.tmp"
    mv -f "${cached_kind}.tmp" "$cached_kind"
  else
    echo "${KIND_SHA256_AMD64}  ${cached_kind}" | sha256sum -c -
  fi
  sudo cp -f "$cached_kind" /usr/local/bin/kind
  sudo chmod 0755 /usr/local/bin/kind
fi
kind version
kubectl version --client

# 2. Ensure pinned Calico manifest.
cached_calico="${CACHE_DIR}/calico-${CALICO_VERSION}.yaml"
if [ ! -f "$cached_calico" ]; then
  echo "Downloading Calico ${CALICO_VERSION} manifest..."
  curl -4 -fsSL --retry 3 --max-time 120 "$CALICO_MANIFEST_URL" -o "${cached_calico}.tmp"
  mv -f "${cached_calico}.tmp" "$cached_calico"
fi
echo "${CALICO_MANIFEST_SHA256}  ${cached_calico}" | sha256sum -c -
cp -f "$cached_calico" "$CALICO_OUT"

# 3. Ensure kind node image in host Docker (restored from cache or pulled).
cached_node_tar="${CACHE_DIR}/kind-node-${KIND_NODE_VERSION}.tar.zst"
if docker image inspect "$KIND_NODE_TAG" >/dev/null 2>&1; then
  echo "kind node image ${KIND_NODE_TAG} already in local Docker"
elif [ -s "$cached_node_tar" ]; then
  echo "Loading kind node image from cache (${cached_node_tar})..."
  zstd -dc "$cached_node_tar" | docker load
else
  echo "Pulling kind node image ${KIND_NODE_PIN}..."
  docker pull --platform linux/amd64 "$KIND_NODE_PIN"
  docker tag "$KIND_NODE_PIN" "$KIND_NODE_TAG"
  docker save --platform linux/amd64 "$KIND_NODE_TAG" | zstd -T0 -1 -f -o "${cached_node_tar}.tmp"
  mv -f "${cached_node_tar}.tmp" "$cached_node_tar"
fi
if [ ! -s "$cached_node_tar" ]; then
  docker save --platform linux/amd64 "$KIND_NODE_TAG" | zstd -T0 -1 -f -o "${cached_node_tar}.tmp"
  mv -f "${cached_node_tar}.tmp" "$cached_node_tar"
fi

# 4. Ensure single-platform workload image archive for `kind load image-archive`.
cached_workload_tar="${CACHE_DIR}/workload-images-${CALICO_VERSION}.tar.zst"
if [ -s "$cached_workload_tar" ]; then
  echo "Decompressing cached workload image archive (${cached_workload_tar})..."
  zstd -d -f "$cached_workload_tar" -o "$WORKLOAD_ARCHIVE"
else
  echo "Pulling workload images and building archive..."
  tags=()
  refs=()
  for entry in "${WORKLOAD_PINS[@]}"; do
    pin="${entry%%|*}"
    tag="${entry##*|}"
    docker pull --platform linux/amd64 "$pin"
    docker tag "$pin" "$tag"
    tags+=("$tag")
    refs+=("$tag" "$pin")
  done
  docker save --platform linux/amd64 -o "$WORKLOAD_ARCHIVE" "${tags[@]}"
  chmod 0644 "$WORKLOAD_ARCHIVE"
  zstd -T0 -1 -f "$WORKLOAD_ARCHIVE" -o "${cached_workload_tar}.tmp"
  chmod 0644 "${cached_workload_tar}.tmp"
  mv -f "${cached_workload_tar}.tmp" "$cached_workload_tar"
  # Free the unpacked workload layers from host Docker now that the archive is written;
  # kind will import directly from WORKLOAD_ARCHIVE into its own containerd.
  docker rmi "${refs[@]}" >/dev/null 2>&1 || true
fi

ls -lh "$CACHE_DIR" "$WORKLOAD_ARCHIVE" "$CALICO_OUT"

if [ -n "${GITHUB_ENV:-}" ]; then
  {
    echo "GRAIN_KIND_NODE_IMAGE=${KIND_NODE_TAG}"
    echo "GRAIN_CALICO_MANIFEST=${CALICO_OUT}"
    echo "GRAIN_K8S_IMAGE_ARCHIVE=${WORKLOAD_ARCHIVE}"
  } >> "$GITHUB_ENV"
fi
