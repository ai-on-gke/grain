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

# §4 item 2 over HTTP: POST /api/pull-requests/submit (what `grain pr
# submit` sends) and POST /api/tasks/{id}/force-merge against a running
#   grain demo -github-sim -fake-ci -review-questions -addr 127.0.0.1:4291 \
#     -access-code-file <file>
# whose task "Publish both daemon images" (acme/widgets#106) holds a
# code-review gate with findings. Every request but the last is refused.
set -u
U=${GRAIN_URL:-http://127.0.0.1:4291}
H="Authorization: Bearer ${GRAIN_CODE:?set GRAIN_CODE to the demo access code}"
PR=${PR:-acme/widgets#106}
TASK=${TASK:-22}

post() {
  echo "--- $2 $3"
  curl -s -o /tmp/merge-gates-resp.json -w "%{http_code} " -H "$H" -H 'Content-Type: application/json' \
    -X POST "$U$1" -d "$3"
  head -c 400 /tmp/merge-gates-resp.json
  echo
}

post /api/pull-requests/submit "missing the review gate:" '{"pr":"'"$PR"'","overrideGates":["security-review"],"reason":"outage"}'
post /api/pull-requests/submit "blank reason:" '{"pr":"'"$PR"'","overrideGates":["review"],"reason":"  "}'
post /api/pull-requests/submit "reason without gates:" '{"pr":"'"$PR"'","reason":"outage"}'
post /api/pull-requests/submit "approval is no gate an override skips:" '{"pr":"'"$PR"'","overrideGates":["review","approval"],"reason":"outage"}'
post /api/pull-requests/submit "a pull request no task owns:" '{"pr":"acme/widgets#999","overrideGates":["review"],"reason":"outage"}'
post "/api/tasks/$TASK/force-merge" "force-merge route, blank reason:" '{"reason":"","overrideGates":["review"]}'
post /api/pull-requests/submit "valid override of the review gate:" '{"pr":"'"$PR"'","overrideGates":["review"],"reason":"walkthrough: release blocker"}'
curl -s -H "$H" "$U/api/tasks/$TASK" | jq -c '{gates: [.mergeGates[] | "\(.id)=\(.state) (\(.detail))"], gateOverride}'
