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

# f8.sh: finding F8 (task 6142). A fresh HEAD daemon, acme/widgets on
# ci_mode: grain, and a catalogue with in_process.grain-ci: {enabled: false}
# written into grain/config's settings/capabilities.yml. A plain task on
# acme/widgets is filed and dispatched; what it holds is read from the store
# and from inside the run (grain ci log), and `grain state check` is run on
# the file.
set -euo pipefail
source "$(dirname "$0")/common.sh"
BIN="${BIN:?BIN is the grain build to walk}"
setup_world
mkdir -p /var/tmp/retired-aliases; echo "$WORK/walk" >/var/tmp/retired-aliases/current-walk-dir
cp "$HERE/hook.py" "$WORK/walk/hook.py"
trap '[[ -f "$WORK/daemon.pid" ]] && stop; kill "$(cat "$WORK/ghsim.pid")" 2>/dev/null || true' EXIT
start_ghsim; env_world; start
CS="$WORK/data/state-repo"
{
echo "== version"; "$BIN" version 2>&1 | head -1
call PUT /api/github-tokens/default '{"value":"ghp_walkthrough_stub"}' >/dev/null
call POST /api/repos '{"repo":"acme/widgets"}' | jq -c '{error}'
call PUT /api/repos/acme/widgets/settings '{"ciMode":"grain"}' | jq -c '{error, ciMode}'
call PUT /api/config '{"defaultCapabilities":[]}' | jq -c '{error, defaultCapabilities}'
stop
printf 'in_process:\n  grain-ci:\n    enabled: false\n' >"$CS/settings/capabilities.yml"
git -C "$CS" add -A && git -C "$CS" -c user.name=w -c user.email=w@example.com commit -qm "disable in_process.grain-ci"
echo "== grain state check"
(cd "$CS" && "$BIN" state check . 2>&1 | head -20) || true
start; sleep 2
echo "-- catalogue as the store holds it"; call GET /api/capability-policy | jq -c '.policy.catalogue // .catalogue'
echo "-- settings/capabilities.yml as re-exported"; cat "$CS/settings/capabilities.yml" 2>/dev/null || echo "(not written)"
echo "== file a plain task on acme/widgets"
WID=$(call POST /api/tasks '{"title":"cat widgets plain","description":"walk","repo":"acme/widgets","approve":false}'); echo "$WID" | jq -c '{id,state,error}'
WID_ID=$(echo "$WID" | jq -r .id)
call GET /api/tasks/$WID_ID | jq -c '{capabilities}'
echo "task_grant rows:"; q "SELECT capability, via FROM task_grant WHERE task_id = '$WID_ID'"
echo "== dispatch"
call PUT /api/config "{\"agentBackends\":[{\"id\":\"ag-walk\",\"name\":\"Walk Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$HERE/fake-agy.sh\",\"models\":[\"fake-model\"]}]}" | jq -c '.error'
call PUT /api/agent-backends/ag-walk/credential '{"value":"walk-fake-gemini-key"}' >/dev/null
call POST /api/tasks/$WID_ID/approve '{}' | jq -c '{id,state,error}'
for _ in $(seq 60); do [[ -f "$WORK/walk/runs/hook-$WID_ID.json" ]] && break; sleep 0.5; done
if [[ -f "$WORK/walk/runs/hook-$WID_ID.json" ]]; then
  jq -c '{tools}' "$WORK/walk/runs/hook-$WID_ID.json"
  jq -r '{ci_log} | to_entries[] | "\(.key): \(.value.text | gsub("\n"; " | ") | .[0:400])"' "$WORK/walk/runs/hook-$WID_ID.json"
else
  echo "no run"; call GET /api/tasks/$WID_ID | jq -c '{state, held: .heldReason, grantWait, last: ([.comments[]?.body] | last | .[0:500])}'
  grep -o "candidate $WID_ID skipped[^\"]*" "$WORK/daemon.log" | sort | uniq -c | head -3
fi
echo "task_grant rows after:"; q "SELECT capability, via FROM task_grant WHERE task_id = '$WID_ID'"
} 2>&1 | tee "$OUT/f8-${STORE:-sqlite}${OUT_SUFFIX:-}.txt"
