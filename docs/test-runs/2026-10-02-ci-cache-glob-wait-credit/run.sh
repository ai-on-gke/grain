#!/bin/sh
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

# run.sh <ref> [push|release] [repo]: start a CI run through the harness and
# print each job's conclusion, flags and step output once it finishes.
# push runs go through the real POST /api/ci/runs; release runs through
# /walk/release (only grain can start one).
S=http://127.0.0.1:18081
ref=$1; ev=${2:-push}; repo=${3:-acme/widgets}
if [ "$ev" = release ]; then
  id=$(curl -s "$S/walk/release?ref=$ref" | jq -r .id)
else
  id=$(curl -s -X POST -H 'Content-Type: application/json' \
    -d "{\"repo\":\"$repo\",\"ref\":\"$ref\",\"event\":\"$ev\"}" $S/api/ci/runs | jq -r .id)
fi
echo "run $id ($repo $ref $ev)"
while :; do
  r=$(curl -s $S/api/ci/runs/$id)
  [ -n "$(printf "%s" "$r" | jq -r '.conclusion // empty')" ] && break
  sleep 1
done
printf "%s" "$r" | jq -r '"conclusion: \(.conclusion) \(.error // "")", (.jobs[] | "job \(.jobId): \(.conclusion) cached=\(.cached // false) keyedOnFiles=\(.keyedOnFiles // 0) quarantined=\(.quarantined // false) status=\(.status // "") error=\(.error // "") warning=\(.warning // "")", (.steps[]? | "  step \(.name): \(.conclusion) exit=\(.exitCode) \(.error // "")\n\(.output // "" | split("\n") | map("    " + .) | join("\n"))"))'
