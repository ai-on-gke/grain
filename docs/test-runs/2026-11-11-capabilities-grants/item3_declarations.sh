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

# item3_declarations.sh: §6 item 3. acme/widgets declares a schedule
# (seed3/widgets/.grain) carrying api#secrets:write, a grant only a person may
# give. Its firing is held until a person accepts the declaration; then it
# lands approved; widening the declaration (network: open on the
# template) holds it again with a note; accepting a stale `seen` set and
# accepting through a task's agent token are refused.
set -euo pipefail
WORK=/var/tmp/capgrants/item3; GHSIM_ADDR=127.0.0.1:4294; PORT=4295
# shellcheck source=common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
SEED_DIR="$HERE/seed3" setup_world
start; configure
call PUT /api/repos/acme/widgets/declarations '{"declarations":true}' >/dev/null
sched=""
for _ in $(seq 60); do
  sched=$(call GET /api/schedules | jq -r '(if type=="array" then . else .schedules end) | map(select(.title=="Declared walk schedule"))[0].id // empty' 2>/dev/null || true)
  [[ -n "$sched" ]] && break; sleep 1
done
echo "declared schedule: $sched"
fire() {
  local before after prev
  prev=$(call GET /api/tasks | jq -r '(if type=="array" then . else .tasks end) | map(select(.title=="Declared walk task")) | max_by(.id|tonumber) | .id // empty')
  [[ -n "$prev" ]] && call POST "/api/tasks/$prev/close" '{}' >/dev/null && sleep 1.5
  before=$(call GET /api/tasks | jq '(if type=="array" then . else .tasks end) | map(select(.title=="Declared walk task")) | length')
  call POST "/api/schedules/$sched/run" '{}' >/dev/null
  for _ in $(seq 40); do
    after=$(call GET /api/tasks | jq '(if type=="array" then . else .tasks end) | map(select(.title=="Declared walk task")) | length')
    [[ "$after" != "$before" ]] && break; sleep 0.5
  done
  call GET /api/tasks | jq -r '(if type=="array" then . else .tasks end) | map(select(.title=="Declared walk task")) | max_by(.id|tonumber) | .id'
}
show() { call GET "/api/tasks/$1" | jq -c '{id,state,approved,capabilities,networkAccess,body:(.description // .body // "")[0:600]}'; }
{
  echo "## firing 1 (no acceptance)"; T1=$(fire); show "$T1"
  echo "## GET /api/declarations"; call GET /api/declarations | jq -c .
  DID=$(call GET /api/declarations | jq -r '.[0].id')
  echo "## accept with a stale seen set (refused)"; call POST /api/declarations/accept "{\"id\":\"$DID\",\"seen\":[]}"; echo
  echo "## accept"; call POST /api/declarations/accept "{\"id\":\"$DID\",\"seen\":$(call GET /api/declarations | jq -c '.[0].requested')}" | jq -c .
  echo "## firing 2 (accepted)"; T2=$(fire); show "$T2"
} >"$OUT/item3-declarations.txt" 2>&1
# Widen: the template now asks for network_access: open as well.
t=$(mktemp -d); git clone -q "$WORK/repos/acme/widgets.git" "$t"
printf 'network: open\n' >>"$t/.grain/templates/walk.yml"
git -C "$t" -c user.name=s -c user.email=s@example.com commit -qam "widen: network_access open"; git -C "$t" push -q origin main; rm -rf "$t"
# The declaration poll runs every two minutes (orchestrator.declarationPollInterval).
for _ in $(seq 200); do
  call GET /api/templates | jq -e '(if type=="array" then . else .templates end) | map(select(.id=="repo:template:acme:widgets:walk"))[0].networkAccess == "open"' >/dev/null && break
  sleep 1
done
{
  echo "## after widening, before a firing"; call GET /api/declarations | jq -c .
  echo "## firing 3 (widened)"; T3=$(fire); show "$T3"
  echo "## GET /api/declarations"; call GET /api/declarations | jq -c .
  echo "## POST /api/declarations/revoke"; call POST /api/declarations/revoke "{\"id\":\"$DID\"}" -o /dev/null -w '%{http_code}\n'
} >>"$OUT/item3-declarations.txt" 2>&1
cat "$OUT/item3-declarations.txt"
stop
