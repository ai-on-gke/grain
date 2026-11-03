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

# §5 item 4: grain's approve route live, on a running
# `grain demo -github-sim -fake-ci` (its access code is a person's:
# human:demo-operator). Re-raises a pending release before each approval,
# since an approval clears its notification.
#
#   GRAIN_SERVER=http://127.0.0.1:<port> GRAIN_TOKEN=<access code> \
#     bash item4-approve-route.sh <grain-binary> [<agent-token>]
set -uo pipefail
grain="$1"
agent_token="${2:-}"
api() { # api <method> <path> [<json>] [<token>]: "<code> <body>"
  local tok="${4:-$GRAIN_TOKEN}"
  curl -s -o /tmp/item4.body -w '%{http_code}' -X "$1" -H "Authorization: Bearer $tok" \
    -H 'Content-Type: application/json' ${3:+-d "$3"} "$GRAIN_SERVER$2"
  printf ' %s\n' "$(head -c 400 /tmp/item4.body)"
}
raise() { # raise <version>: a release.pending notification like the train's
  api POST /api/notifications "$(jq -cn --arg v "$1" '{subject: ("release.pending:" + $v), repo: "ai-on-gke/grain",
    title: ($v + " is ready to promote"), actions: ["approve-release", "reject-release"],
    body: ("From " + $v + "-rc.1.\n\nSoak: soaked 1h00m of 24h (FORCE=true)\nSHORT SOAK: under the 22h minimum; approving it needs short_soak_ok: true.")}')" >/dev/null
}
base=/api/repos/ai-on-gke/grain/pending-releases

echo "== 4a the route with short_soak_ok, as the person the access code is"
raise v2.8.0
api POST "$base/v2.8.0/approve" '{"short_soak_ok":true}'
echo "== 4b reject with short_soak_ok is refused"
api POST "$base/v2.8.0/reject" '{"short_soak_ok":true}'
echo "== 4c grain release approve -short-soak-ok v2.9.0"
raise v2.9.0
"$grain" release approve -short-soak-ok v2.9.0; echo "   exit $?"
echo "== 4d grain release reject -short-soak-ok v2.9.0 (refused by the CLI)"
"$grain" release reject -short-soak-ok v2.9.0; echo "   exit $?"
echo "== 4e the promote-release runs the decisions started (GRAIN_CI_RELEASE_SHORT_SOAK_OK on each)"
api GET "/api/ci/runs?limit=20" | cut -c1-4
jq -c '[.runs[]? // .[]? | select(.workflowPath? // .workflow? // "" | tostring | test("promote-release")) | {id, ref, event, requestedBy, requestedByKind, releaseVersion, releaseShortSoakOk, jobs: [.jobs[]?.jobId]}] | .[:6][]' /tmp/item4.body 2>/dev/null | head -n 8
if [ -n "$agent_token" ]; then
  echo "== 4f an agent token is refused"
  raise v2.10.0
  api POST "$base/v2.10.0/approve" '{"short_soak_ok":true}' "$agent_token"
fi
echo "== 4g a patch release the train stages (vX.Y.Z-pending, Z >= 1)"
api POST "$base/v3.1.1/approve" '{}'
GRAIN_SERVER="$GRAIN_SERVER" "$grain" release approve v3.1.1; echo "   exit $?"
echo "== 4h not a version"
api POST "$base/latest/approve" '{}'
