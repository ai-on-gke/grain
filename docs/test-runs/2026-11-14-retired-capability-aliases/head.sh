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

# head.sh: after seed.sh, write bare names straight into the closed store
# (the rows the plan worries about: nothing on v3.5.0's API stores them, so
# this is how one gets there), open it with HEAD (BIN=grain-head), and record
# each row's stored value, API view and what a dispatched run receives.
set -euo pipefail
source "$(dirname "$0")/common.sh"
(cd "$ROOT" && go build -o "$WORK/ghsim" ./docs/test-runs/2026-11-14-retired-capability-aliases/ghsim.go)
mkdir -p /var/tmp/retired-aliases; echo "$WORK/walk" >/var/tmp/retired-aliases/current-walk-dir
cp "$HERE/hook.py" "$WORK/walk/hook.py"; rm -f "$WORK/walk/runs/"*
# Task 10: one row per bare name, as written by hand or by a path that did
# not canonicalise. Deployment defaults and acme/widgets CI gain grain-ci.
for n in $(echo "$BARE" | jq -r '.[]'); do q "INSERT INTO task_grant (task_id, capability, via, folder) VALUES ('9', '$n', 'label', '')"; done
q "UPDATE grain_config SET default_capabilities = default_capabilities || ',grain-ci'"
q "UPDATE repo_config SET ci_capabilities = 'grain-ci,self-debug,https://grain.dev/capabilities/release#registry-push={}' WHERE owner='acme' AND name='widgets'"
[[ -f "$WORK/ghsim.pid" ]] && kill "$(cat "$WORK/ghsim.pid")" 2>/dev/null || true; sleep 0.5
start_ghsim; env_world; start
{
echo "== version"; "$BIN" version 2>&1 | head -1
echo "== schema"; q "SELECT * FROM grain_schema"
echo "== raw rows after HEAD opened the store"
for t in task_grant template_grant schedule_grant; do echo "-- $t"; q "SELECT * FROM $t WHERE $( [[ $t == schedule_grant ]] && echo "schedule_id NOT LIKE 'system:%'" || echo "1=1") ORDER BY 1,2"; done
echo "-- grain_config.default_capabilities"; q "SELECT default_capabilities FROM grain_config"
echo "-- repo_config"; q "SELECT owner, name, default_capabilities, ci_capabilities FROM repo_config"
echo "== GET /api/tasks/{id}"
for id in 2 3 4 5 6 7 8 9; do printf '%s ' "$id"; call GET /api/tasks/$id | jq -c '{title, capabilities, grants: [.grants[]? | (.capability // .)]}'; done
echo "== GET /api/templates/template-1"; call GET /api/templates/template-1 | jq -c '{capabilities, grants}'
echo "== GET /api/schedules/sched-1"; call GET /api/schedules/sched-1 | jq -c '{capabilities, grants}'
echo "== GET /api/config defaultCapabilities"; call GET /api/config | jq -c '.defaultCapabilities'
echo "== GET /api/repos/acme/widgets/settings"; call GET /api/repos/acme/widgets/settings | jq -c '{ciMode, ciCapabilities, defaultCapabilities}'
echo "== dispatch"
call PUT /api/config "{\"agentBackends\":[{\"id\":\"ag-walk\",\"name\":\"Walk Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$HERE/fake-agy.sh\",\"models\":[\"fake-model\"]}]}" | jq -c '.error'
call PUT /api/agent-backends/ag-walk/credential '{"value":"walk-fake-gemini-key"}' >/dev/null
for id in ${DISPATCH:-4 7 9}; do call POST /api/tasks/$id/approve '{}' | jq -c '{id,state,error}'; done
for id in ${DISPATCH:-4 7 9}; do
  for _ in $(seq 360); do [[ -f "$WORK/walk/runs/hook-$id.json" ]] && break; sleep 0.5; done
  echo "-- task $id"; jq -c '{tools}' "$WORK/walk/runs/hook-$id.json" 2>/dev/null || echo "no hook output"
  jq -c 'del(.tools)' "$WORK/walk/runs/hook-$id.json" 2>/dev/null || true
done
echo "-- what the refused runs were told"
for id in 8 9; do printf '%s ' "$id"; call GET /api/tasks/$id | jq -c '{state, lastComment: ([.comments[]?.body] | last | .[0:600])}'; done
echo "-- proposals the runs filed (propose_task named self-debug, grain-ci)"
sleep 3; call GET '/api/tasks?limit=100' | jq -c '[.[]? // .tasks[]? | select(.title == "walk: bare names") | {id, state}]'
for id in $(call GET '/api/tasks?limit=100' | jq -r '[.[]? // .tasks[]? | select(.title == "walk: bare names") | .id] | .[]'); do call GET /api/tasks/$id | jq -c '{id, capabilities, comments: [.comments[]?.body | .[0:400]]}'; done
echo "-- simulator git credentials"; grep 'ghsim git' "$WORK/ghsim.log" | grep -v 'credential=""' | sed 's/credential="\(ghp_[a-z_]*\).*/credential=\1/' | sort | uniq -c
} 2>&1
[[ -n "${KEEP:-}" ]] || stop
