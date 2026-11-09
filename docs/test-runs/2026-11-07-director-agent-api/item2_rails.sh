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

# §4 item 2: the adversarial proposal & approval rail matrix, against the
# daemon env.sh started (the Director is task 1 on a fresh store).
#
# Director proposals are replayed the way the control plane relays a live
# propose_task call (POST /api/tasks with proposedBy, which only the
# daemon-trusted access code may name); the Director's own agent-API calls
# carry X-Grain-Acting-Task: 1; the external agent is a gma_ credential.
set -uo pipefail
# shellcheck source=/dev/null
source "${WALKTHROUGH_DIR:-/var/tmp/grain-walkthrough-3488}/vars.env"
AC=(-H "X-Grain-Access-Code: ${ACCESS_CODE}" -H "Content-Type: application/json")
DIRECTOR=(-H "X-Grain-Acting-Task: 1" -H "X-Grain-Acting-Reason: walkthrough")

say() { printf '\n## %s\n' "$*"; }
# call METHOD PATH [BODY] [extra curl args...]: prints "HTTP <code> <json summary>".
call() {
  local method="$1" path="$2" body="${3:-}"
  shift 3 || shift $#
  local out code
  out="$(curl -sS -o /tmp/rails.body -w '%{http_code}' -X "$method" "${UI_URL}${path}" "${AC[@]}" "$@" ${body:+-d "$body"})"
  code="$out"
  printf 'HTTP %s %s\n' "$code" "$(jq -c '{id,state,admission,error}|with_entries(select(.value!=null))' /tmp/rails.body 2>/dev/null || head -c 300 /tmp/rails.body)"
}
propose() { # propose TITLE JSON-EXTRA
  call POST /api/tasks "{\"title\":\"$1\",\"body\":\"walkthrough\",\"proposedBy\":\"1\"${2:+,$2}}"
}
last_note() { # the newest grain comment on a task, first 220 bytes
  curl -sS "${UI_URL}/api/tasks/$1" "${AC[@]}" | jq -r '[.comments[]?|.body]|last // ""' | tr '\n' ' ' | cut -c1-220
  echo
}

say "setup: a person's message on the Director starts a fresh turn; acme/other seeds a default grant"
call POST /api/tasks/1/comments '{"body":"walkthrough: start a new turn"}'
call PUT /api/repos/acme/other/capabilities '{"defaultCapabilities":["https://grain.dev/capabilities/github#read-issues={}"]}'

say "A. unchanged seeded repo default (acme/other's read-issues, which the Director does not hold) -> queued"
propose "seeded default" '"repo":"acme/other"'
say "B. smuggled Tier 3 api scope (api:settings:write) -> held or refused"
propose "tier3 api" '"repo":"acme/app","capabilities":["api:settings:write"]'
say "C. smuggled person-only capability (grain#host-exec) -> held"
propose "host-exec" '"repo":"acme/app","capabilities":["https://grain.dev/capabilities/grain#host-exec={}"]'
say "D. admission durable -> held"
propose "durable" '"repo":"acme/app","admission":"durable"'
say "E. admission bypass -> held"
propose "bypass" '"repo":"acme/app","admission":"bypass"'
say "F. targets grain/config -> held"
propose "config target" '"repo":"grain/config"'
say "G. writes grain/config -> held"
propose "config write" '"repo":"acme/app","writes":[{"repo":"grain/config"}]'
say "H. seeded template carrying host-exec (system:gke-sandbox-cluster) -> held"
propose "template host-exec" '"repo":"acme/app","templateId":"system:gke-sandbox-cluster"'
say "I. holdForApproval -> held"
propose "hold" '"repo":"acme/app","holdForApproval":true'
for t in $(curl -sS "${UI_URL}/api/tasks?limit=50" "${AC[@]}" | jq -r 'if type=="array" then . else .tasks end | .[] | select(.title|test("^(seeded default|tier3 api|host-exec|durable|bypass|config target|config write|template host-exec|hold)$")) | .id' | sort -n); do
  printf 'task %s: ' "$t"
  curl -sS "${UI_URL}/api/tasks/$t" "${AC[@]}" | jq -r '"\(.title) -> \(.state) caps=\(.capabilities|length) admission=\(.admission)"'
  printf '  note: '; last_note "$t"
done

say "per-turn cap: 11 more proposals in the same turn (TurnFanOut 10; A above already counts)"
for i in $(seq 1 11); do propose "fan-out $i" '"repo":"acme/app"'; done

say "Director self-approval of its own held proposal (the last fan-out)"
OWN="$(curl -sS "${UI_URL}/api/tasks?limit=100" "${AC[@]}" | jq -r 'if type=="array" then . else .tasks end | [.[]|select(.title=="fan-out 11")][0].id')"
echo "own held proposal: task ${OWN} ($(curl -sS "${UI_URL}/api/tasks/${OWN}" "${AC[@]}" | jq -r .state))"
echo "- POST /api/tasks/${OWN}/approve:"; call POST "/api/tasks/${OWN}/approve" '{"reason":"walkthrough"}' "${DIRECTOR[@]}"
echo "- POST /api/tasks/${OWN}/run-now:"; call POST "/api/tasks/${OWN}/run-now" '{"reason":"walkthrough"}' "${DIRECTOR[@]}"
echo "- PATCH /api/tasks/${OWN} {approved:true}:"; call PATCH "/api/tasks/${OWN}" '{"approved":true}' "${DIRECTOR[@]}"
echo "- POST /api/tasks {approved:true} as the Director:"; call POST /api/tasks '{"title":"self approved","repo":"acme/app","approved":true}' "${DIRECTOR[@]}"

director_person_matrix() {
# holdForApproval means nothing without proposedBy (CreateTaskRequest), so the
# held-for-approval case is a proposal by a third task outside the Director's
# lineage: an approved person-filed task proposes it with hold_for_approval.
PARENT="$(curl -sS -X POST "${UI_URL}/api/tasks" "${AC[@]}" -d '{"title":"third-party parent","repo":"acme/app","approved":true}' | jq -r .id)"
HOLD="$(curl -sS -X POST "${UI_URL}/api/tasks" "${AC[@]}" -d "{\"title\":\"third-party hold\",\"repo\":\"acme/app\",\"proposedBy\":\"${PARENT}\",\"holdForApproval\":true}" | jq -r .id)"
TIER3="$(curl -sS -X POST "${UI_URL}/api/tasks" "${AC[@]}" -d "{\"title\":\"third-party tier3\",\"repo\":\"acme/app\",\"proposedBy\":\"${PARENT}\",\"capabilities\":[\"api:settings:write\"]}" | jq -r .id)"
DUR="$(curl -sS -X POST "${UI_URL}/api/tasks" "${AC[@]}" -d '{"title":"person durable","repo":"acme/app","approved":false,"admission":"durable"}' | jq -r .id)"
CFG="$(curl -sS -X POST "${UI_URL}/api/tasks" "${AC[@]}" -d '{"title":"person config","repo":"grain/config","approved":false}' | jq -r .id)"
PLAIN="$(curl -sS -X POST "${UI_URL}/api/tasks" "${AC[@]}" -d '{"title":"person plain","repo":"acme/app","approved":false}' | jq -r .id)"
echo "parent=${PARENT} hold=${HOLD} tier3=${TIER3} durable=${DUR} config=${CFG} plain=${PLAIN}"
for t in "$HOLD" "$TIER3" "$DUR" "$CFG"; do
  echo "- approve ${t}:"; call POST "/api/tasks/${t}/approve" '{"reason":"walkthrough"}' "${DIRECTOR[@]}"
  echo "- run-now ${t}:"; call POST "/api/tasks/${t}/run-now" '{"reason":"walkthrough"}' "${DIRECTOR[@]}"
done
echo "- approve plain ${PLAIN} (held for nobody, outside the lineage):"; call POST "/api/tasks/${PLAIN}/approve" '{"reason":"walkthrough"}' "${DIRECTOR[@]}"
}
say "Person-filed tasks the Director tries to approve at its shipped posture (api:task-admin, no tasks:admin/dispatch:write)"
director_person_matrix

say "A person grants the Director api:tasks:admin + api:dispatch:write; the G8 rail must still hold"
CAPS="$(curl -sS "${UI_URL}/api/console-sessions" "${AC[@]}" | jq -c '[.sessions[]|select(.id=="director")][0].capabilities + ["api:tasks:admin","api:dispatch:write"]')"
call PUT /api/console-sessions/director "{\"capabilities\":${CAPS}}"
curl -sS "${UI_URL}/api/tasks/1" "${AC[@]}" | jq -c '{task1caps:[.capabilities[]|select(test("tasks:admin|dispatch:write"))]}'
echo "- own held proposal ${OWN} again: approve / run-now"
call POST "/api/tasks/${OWN}/approve" '' "${DIRECTOR[@]}"
call POST "/api/tasks/${OWN}/run-now" '' "${DIRECTOR[@]}"
director_person_matrix
for t in "$HOLD" "$TIER3" "$DUR" "$CFG" "$PLAIN" "$OWN"; do
  printf 'task %s now: ' "$t"; curl -sS "${UI_URL}/api/tasks/$t" "${AC[@]}" | jq -r '"\(.title) -> \(.state)"'
done

say "External agent credential (tasks:read/write/admin + dispatch:write on acme/app)"
MINT="$(curl -sS -X POST "${UI_URL}/api/external-agents/tokens" "${AC[@]}" -d '{"name":"rails-ext","scopes":["tasks:read","tasks:write","tasks:admin","dispatch:write"],"repos":["acme/app"],"owner":"alice@example.com"}')"
GMA="$(jq -r .token <<<"$MINT")"
echo "minted: $(jq -c '{id,owner,resolvedScopes,error}' <<<"$MINT")"
EXT=(-H "Authorization: Bearer ${GMA}" -H "Content-Type: application/json" -H "X-Grain-Acting-Reason: walkthrough")
ext() { # ext METHOD PATH BODY
  local code
  code="$(curl -sS -o /tmp/rails.body -w '%{http_code}' -X "$1" "${UI_URL}$2" "${EXT[@]}" ${3:+-d "$3"})"
  printf 'HTTP %s %s\n' "$code" "$(jq -c '{id,state,error}|with_entries(select(.value!=null))' /tmp/rails.body 2>/dev/null || head -c 300 /tmp/rails.body)"
}
HOLD2="$(curl -sS -X POST "${UI_URL}/api/tasks" "${AC[@]}" -d '{"title":"person hold 2","repo":"acme/app","approved":false,"holdForApproval":true}' | jq -r .id)"
DUR2="$(curl -sS -X POST "${UI_URL}/api/tasks" "${AC[@]}" -d '{"title":"person durable 2","repo":"acme/app","approved":false,"admission":"durable"}' | jq -r .id)"
echo "- external files a task with approved:true:"; ext POST /api/tasks '{"title":"ext self approved","repo":"acme/app","approved":true}'
OWNEXT="$(curl -sS -X POST "${UI_URL}/api/tasks" "${EXT[@]}" -d '{"title":"ext filed","repo":"acme/app"}' | jq -r .id)"
echo "- external filed task ${OWNEXT} ($(curl -sS "${UI_URL}/api/tasks/${OWNEXT}" "${AC[@]}" | jq -r .state)); approve it:"; ext POST "/api/tasks/${OWNEXT}/approve" '{"reason":"walkthrough"}'
echo "- approve person hold ${HOLD2}:"; ext POST "/api/tasks/${HOLD2}/approve" '{"reason":"walkthrough"}'
echo "- run-now person durable ${DUR2}:"; ext POST "/api/tasks/${DUR2}/run-now" '{"reason":"walkthrough"}'
echo "- same over POST /api/mcp (grain_invoke api_approve on ${DUR2}):"
curl -sS -X POST "${UI_URL}/api/mcp" "${EXT[@]}" -H "Accept: application/json, text/event-stream" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"grain_invoke\",\"arguments\":{\"tool\":\"api_approve\",\"arguments\":{\"id\":\"${DUR2}\"},\"reason\":\"walkthrough\"}}}" |
  sed -n 's/^data: //p;/^{/p' | jq -c '.result.isError, (.result.content[0].text|.[0:240]), .error' 2>/dev/null
for t in "$HOLD2" "$DUR2" "$OWNEXT"; do
  printf 'task %s now: ' "$t"; curl -sS "${UI_URL}/api/tasks/$t" "${AC[@]}" | jq -r '"\(.title) -> \(.state)"'
done
