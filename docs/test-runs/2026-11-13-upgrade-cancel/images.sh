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

# images.sh starts a registry:2 on localhost:5000 (if one is not already
# there) and pushes the test images the upgrade-cancel walkthrough
# upgrades between, each the HEAD grain binary on debian:bookworm-slim
# with `grain` as its ENTRYPOINT:
#
#   :v1    what the daemon under test "runs" (its ref file names it)
#   :v2    an ordinary upgrade target; its health check answers at once
#   :slow  an upgrade target whose health check sleeps $SLOW seconds
#          before running `grain schema-version`, so a cancel can land
#          while the check's container is up (staging, §10 item 1)
#
# The images are removed from the local docker store after the push, so
# an upgrade really pulls them and a cancel has something to remove.
set -euo pipefail
OUT=${OUT:-/tmp/imgs}
REG=${REG:-localhost:5000/acme/grain}
GRAIN=${GRAIN:-/home/debian/bin/grain}
SLOW=${SLOW:-60}
if ! curl -sf http://localhost:5000/v2/ >/dev/null; then
	docker run -d --name registry -p 5000:5000 registry:2 >/dev/null
	for _ in $(seq 30); do curl -sf http://localhost:5000/v2/ >/dev/null && break; sleep 1; done
fi
mkdir -p "$OUT"
cp "$GRAIN" "$OUT/grain"
cd "$OUT"
cat >Dockerfile <<'EOF'
FROM debian:bookworm-slim
COPY grain /usr/local/bin/grain
ENTRYPOINT ["/usr/local/bin/grain"]
EOF
cat >Dockerfile.slow <<EOF
FROM debian:bookworm-slim
COPY grain /usr/local/bin/grain
ENTRYPOINT ["/bin/sh", "-c", "sleep $SLOW; exec /usr/local/bin/grain \"\$@\"", "grain"]
EOF
for tag in v1 v2 slow; do
	file=Dockerfile
	[ "$tag" = slow ] && file=Dockerfile.slow
	# A distinct label per tag, so each tag is its own image and the
	# cancel's `docker image rm` of one never touches another.
	docker build -q -f "$file" --label "walk.tag=$tag" -t "$REG:$tag" . >/dev/null
	docker push -q "$REG:$tag" >/dev/null
	echo "$tag $(docker inspect --format '{{index .RepoDigests 0}}' "$REG:$tag")"
done
# v1 stays (the daemon "runs" it); the targets must be pulled.
docker image rm "$REG:v2" "$REG:slow" >/dev/null
