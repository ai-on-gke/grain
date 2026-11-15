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

# images.sh builds one test image per revision forge.go wrote to
# $FORGE/revs.env, each the HEAD grain binary on debian:bookworm-slim
# with org.opencontainers.image.revision set to that revision, pushes
# them to the local registry:2 on localhost:5000 and signs them with a
# throwaway cosign key ($OUT/cosign.key, empty password). It also pushes
# a multi-platform index whose two platforms name different revisions,
# and an unsigned image, for the refusal cases.
set -euo pipefail
FORGE=${FORGE:-/tmp/forge}
OUT=${OUT:-/tmp/imgs}
REG=${REG:-localhost:5000/acme/grain}
GRAIN=${GRAIN:-/home/debian/bin/grain}
# shellcheck disable=SC1091
. "$FORGE/revs.env"
mkdir -p "$OUT"
cp "$GRAIN" "$OUT/grain"
cd "$OUT"
cat >Dockerfile <<'EOF'
FROM debian:bookworm-slim
ARG REVISION
COPY grain /usr/local/bin/grain
LABEL org.opencontainers.image.revision=$REVISION
ENTRYPOINT ["/usr/local/bin/grain"]
EOF
[ -f cosign.key ] || COSIGN_PASSWORD='' cosign generate-key-pair >/dev/null
for pair in "reviewed:$REVIEWED" "selfmerged:$SELFMERGED" "stale:$STALE" "unsigned:$REVIEWED"; do
	tag=${pair%%:*} rev=${pair#*:}
	docker build -q --build-arg REVISION="$rev" -t "$REG:$tag" . >/dev/null
	docker push -q "$REG:$tag" >/dev/null
	digest=$(docker inspect --format '{{index .RepoDigests 0}}' "$REG:$tag")
	if [ "$tag" != unsigned ]; then
		COSIGN_PASSWORD='' cosign sign -y --key cosign.key --tlog-upload=false \
			--allow-insecure-registry "$digest" >/dev/null 2>&1
	fi
	echo "$tag $rev $digest"
done
# A two-platform index whose platforms disagree about the revision:
# amd64 is the reviewed image's manifest, "arm64" the self-merged one's.
# Docker's containerd store pushes each tag as an index already, so the
# platform manifests are read out of those and a new index PUT by hand.
API=http://${REG%%/*}/v2/${REG#*/}
ACCEPT='Accept: application/vnd.oci.image.index.v1+json'
amd=$(curl -s -H "$ACCEPT" "$API/manifests/reviewed" | jq -c '.manifests[] | select(.platform.architecture=="amd64") | {mediaType,digest,size}')
arm=$(curl -s -H "$ACCEPT" "$API/manifests/selfmerged" | jq -c '.manifests[] | select(.platform.architecture=="amd64") | {mediaType,digest,size}')
jq -cn --argjson a "$amd" --argjson b "$arm" '{schemaVersion:2,mediaType:"application/vnd.oci.image.index.v1+json",
	manifests:[$a+{platform:{architecture:"amd64",os:"linux"}}, $b+{platform:{architecture:"arm64",os:"linux"}}]}' >mixed.json
curl -sf -X PUT -H 'Content-Type: application/vnd.oci.image.index.v1+json' --data-binary @mixed.json "$API/manifests/mixed"
mixed=sha256:$(sha256sum mixed.json | cut -d' ' -f1)
COSIGN_PASSWORD='' cosign sign -y --key cosign.key --tlog-upload=false \
	--allow-insecure-registry "$REG@$mixed" >/dev/null 2>&1
echo "mixed index $REG@$mixed"
