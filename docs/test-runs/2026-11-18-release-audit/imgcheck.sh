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
#
# Every promised image of a release at its version, sha-, latest, main,
# candidate and rc tags on Docker Hub: manifest-list digest, and for the
# daemon images the revision and version labels from the image config.
# Read-only (docker buildx imagetools inspect; no login).
# Usage: imgcheck.sh <vX.Y.Z> <short sha>
# usage: imgcheck.sh <version> <short>
V=$1; S=$2
dig() { docker buildx imagetools inspect "$1" --format '{{json .Manifest}}' 2>/dev/null | jq -r .digest; }
lab() { docker buildx imagetools inspect "$1" --format '{{json .Image}}' 2>/dev/null | jq -c 'if has("linux/amd64") then .["linux/amd64"].config.Labels else .config.Labels end | {rev:.["org.opencontainers.image.revision"],ver:.["org.opencontainers.image.version"]}'; }
for i in grain hub registry sandbox sandbox-k8s; do
  for t in $V sha-$S latest main candidate rc; do
    printf '%-12s %-16s %s %s\n' $i $t "$(dig docker.io/grainai/$i:$t || echo MISSING)" "$( [[ $i =~ ^(grain|hub|registry)$ ]] && lab docker.io/grainai/$i:$t)"
  done
done
