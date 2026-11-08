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

# §4 item 1: fresh posture, an upgraded (pre-#806/#817) Director row, and
# Adopt shipped defaults over REST, including concurrent edits racing the
# adopt. The UI half is ui_adopt.mjs. Uses the daemon env.sh started.
set -uo pipefail
# shellcheck source=/dev/null
source "${WALKTHROUGH_DIR:-/var/tmp/grain-walkthrough-3488}/vars.env"
AC=(-H "X-Grain-Access-Code: ${ACCESS_CODE}" -H "Content-Type: application/json")
director() { curl -sS "${UI_URL}/api/console-sessions" "${AC[@]}" | jq -c '[.sessions[]|select(.id=="director")][0]'; }

echo "## fresh store"
director | jq -c '{networkAccess,effectiveNetworkAccess,tasksApproved,taskId,tier1:[.capabilities[]|select(startswith("api:"))],missingShippedDefaults}'
curl -sS "${UI_URL}/api/tasks/1" "${AC[@]}" | jq -c '{task1:{networkAccess,approved:(.approvedBy!=null)}}'

echo "## upgraded store: a saved pre-#806/#817 row (no Tier 1 api grants, tasksApproved false, one custom extra)"
OLD='["api:ci-read","api:deployment:read","api:self-debug","https://grain.dev/capabilities/config-map#config-map={\"config_maps\":[\"bootstrap\"]}","https://grain.dev/capabilities/grain#health={}","https://grain.dev/capabilities/grain#journal={}","https://grain.dev/capabilities/grain#source={}","https://grain.dev/capabilities/github#read-issues={}"]'
curl -sS -X PUT "${UI_URL}/api/console-sessions/director" "${AC[@]}" -d "{\"capabilities\":${OLD},\"tasksApproved\":false}" -o /dev/null -w 'PUT %{http_code}\n'
director | jq -c '{tasksApproved,held:(.capabilities|length),missingShippedDefaults}'

echo "## adopt over REST (what the pane's button sends: held + missing), racing 10 concurrent tasksApproved/title edits"
CUR="$(director)"
NEXT="$(jq -c '(.capabilities + .missingShippedDefaults) | unique' <<<"$CUR")"
for i in $(seq 1 10); do
  curl -sS -X PUT "${UI_URL}/api/console-sessions/director" "${AC[@]}" -d "{\"title\":\"Director ${i}\"}" -o /dev/null -w "title-${i} %{http_code}\n" &
done
curl -sS -X PUT "${UI_URL}/api/console-sessions/director" "${AC[@]}" -d "{\"capabilities\":${NEXT}}" -o /dev/null -w 'adopt %{http_code}\n' &
wait
sleep 3 # one reconcile cycle (-poll-interval 2s)
AFTER="$(director)"
jq -c '{tasksApproved,held:(.capabilities|length),missingShippedDefaults,customExtraKept:(.capabilities|index("https://grain.dev/capabilities/github#read-issues={}")!=null)}' <<<"$AFTER"
echo "## the Director's task carries every session grant (no grant dropped by the sync)"
TASKCAPS="$(curl -sS "${UI_URL}/api/tasks/1" "${AC[@]}" | jq -c '.capabilities|sort')"
jq -n --argjson s "$(jq -c '.capabilities|sort' <<<"$AFTER")" --argjson t "$TASKCAPS" \
  '{sessionOnly:($s-$t), taskOnly:($t-$s)}'
grep -c "overwrote\|lost update\|moved since" "$DAEMON_LOG" | sed 's/^/daemon log sync-race lines: /'

echo "## stale-tab adopt: tab A reads the row, tab B adds a grant, tab A clicks Adopt"
curl -sS -X PUT "${UI_URL}/api/console-sessions/director" "${AC[@]}" -d "{\"capabilities\":${OLD}}" -o /dev/null -w 'reset to the pre-#806 row: %{http_code}\n'
TAB_A="$(director)"
curl -sS -X PUT "${UI_URL}/api/console-sessions/director" "${AC[@]}" \
  -d "$(jq -c '{capabilities:(.capabilities + ["api:tasks:read"])}' <<<"$TAB_A")" -o /dev/null -w 'tab B adds api:tasks:read: %{http_code}\n'
curl -sS -X PUT "${UI_URL}/api/console-sessions/director" "${AC[@]}" \
  -d "$(jq -c '{capabilities:((.capabilities + .missingShippedDefaults)|unique)}' <<<"$TAB_A")" -o /dev/null -w 'tab A adopts from its stale read: %{http_code}\n'
director | jq -c '{tabBGrantKept:(.capabilities|index("api:tasks:read")!=null), missingShippedDefaults}'
