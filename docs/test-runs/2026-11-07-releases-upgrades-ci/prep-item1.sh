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

# prep-item1.sh builds what run-item1.sh serves: two tiny runnable
# docker.io/grainai/grain:v3.2.0 images (FROM scratch + a static grain binary,
# differing only in a marker file so their image IDs differ), their gzipped
# `docker save` tarballs, manifest-meta.env naming both image IDs, and the
# releases_forge binary. Everything lands in /tmp/item1-build; nothing is
# written to the checkout.
set -euo pipefail

REPO="${REPO:-$(git -C "$(dirname "$0")" rev-parse --show-toplevel)}"
OUT="${OUT:-/tmp/item1-build}"
HERE="$(cd "$(dirname "$0")" && pwd)"

rm -rf "$OUT"
mkdir -p "$OUT/ctx"
(cd "$REPO" && CGO_ENABLED=0 go build -o "$OUT/ctx/grain" ./cmd/grain)
(cd "$REPO" && go build -o "$OUT/releases_forge" "$HERE/releases_forge.go")

cat >"$OUT/ctx/Dockerfile" <<'EOF'
FROM scratch
COPY grain /usr/local/bin/grain
COPY marker /marker
ENTRYPOINT ["/usr/local/bin/grain"]
CMD ["daemon"]
EOF

build_one() {
  local marker="$1" tarball="$2"
  printf '%s\n' "$marker" >"$OUT/ctx/marker"
  docker build -q -t docker.io/grainai/grain:v3.2.0 "$OUT/ctx" >/dev/null
  local id
  id=$(docker image inspect --format '{{.Id}}' docker.io/grainai/grain:v3.2.0)
  docker save docker.io/grainai/grain:v3.2.0 | gzip -1 >"$tarball"
  docker rmi -f docker.io/grainai/grain:v3.2.0 "$id" >/dev/null 2>&1 || true
  printf '%s' "$id"
}

IMG_ID_1=$(build_one primary "$OUT/grain-image.tar.gz")
IMG_ID_2=$(build_one swapped "$OUT/grain-image-swapped.tar.gz")
cat >"$OUT/manifest-meta.env" <<EOF
IMG_ID_1=$IMG_ID_1
IMG_ID_2=$IMG_ID_2
EOF
cat "$OUT/manifest-meta.env"
