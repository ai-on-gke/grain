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

# f5.sh: F5 on a fresh world. One task on acme/lib is dispatched; hook.py
# calls propose_task naming self-debug and grain-ci inside its run. Then
# follow the proposal past the run's end: what the call answered, whether
# a task was filed, what it holds, and what the proposer was told.
set -euo pipefail
source "$(dirname "$0")/common.sh"
setup_world
mkdir -p /var/tmp/retired-aliases; echo "$WORK/walk" >/var/tmp/retired-aliases/current-walk-dir
cp "$HERE/hook.py" "$WORK/walk/hook.py"
trap '[[ -f "$WORK/daemon.pid" ]] && stop; kill "$(cat "$WORK/ghsim.pid")" 2>/dev/null || true' EXIT
start_ghsim; env_world; start
{
echo "== version"; "$BIN" version 2>&1 | head -1
call PUT /api/github-tokens/default '{"value":"ghp_walkthrough_stub"}' >/dev/null
call POST /api/repos '{"repo":"acme/lib"}' >/dev/null
stop; start   # a new GitHub credential is offered only after a restart
call PUT /api/config "{\"agentBackends\":[{\"id\":\"ag-walk\",\"name\":\"Walk Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$HERE/fake-agy.sh\",\"models\":[\"fake-model\"]}]}" | jq -c '.error'
call PUT /api/agent-backends/ag-walk/credential '{"value":"walk-fake-gemini-key"}' >/dev/null
R=$(call POST /api/tasks '{"title":"f5 proposer","description":"walk","repo":"acme/lib","approve":true}'); echo "$R" | jq -c '{id,state,error}'
ID=$(echo "$R" | jq -r .id)
call POST /api/tasks/$ID/approve '{}' | jq -c '{id,state,error}'
echo "proposer: task $ID"
for _ in $(seq 360); do [[ -f "$WORK/walk/runs/hook-$ID.json" ]] && break; sleep 0.5; done
printf 'propose_task answered: '; jq -c '.propose_bare' "$WORK/walk/runs/hook-$ID.json"
for _ in $(seq 120); do s=$(call GET /api/tasks/$ID | jq -r .state); [[ "$s" != running && "$s" != queued ]] && break; sleep 1; done
sleep 5
echo "proposer state once the run ended: $(call GET /api/tasks/$ID | jq -r .state)"
echo "-- every task now"
call GET '/api/tasks?limit=50' | jq -c '[.[]? // .tasks[]? | {id, title, state}]'
for pid in $(call GET '/api/tasks?limit=50' | jq -r '[.[]? // .tasks[]? | select(.title == "walk: bare names") | .id] | .[]'); do
  echo "-- the filed proposal, task $pid"
  call GET /api/tasks/$pid | jq -c '{state, approved: (.approval != null), capabilities, grants: [.grants[]? | (.capability // .)]}'
  call GET /api/tasks/$pid | jq -r '.description'
  echo "-- a person approves task $pid; what dispatch does with it"
  call POST /api/tasks/$pid/approve '{}' | jq -c '{id,state,error}'
  for _ in $(seq 60); do s=$(call GET /api/tasks/$pid | jq -r .state); [[ "$s" != queued && "$s" != running ]] && break; sleep 1; done
  call GET /api/tasks/$pid | jq -c '{state, last: ([.comments[]?.body] | last | .[0:700])}'
done
echo "-- what the proposer's conversation says"
call GET /api/tasks/$ID | jq -r '[.comments[]?.body] | .[]'
echo "-- daemon log about proposals"; grep -i 'propos' "$WORK/daemon.log" | cut -c1-300 | head -6 || true
} 2>&1
stop; kill "$(cat "$WORK/ghsim.pid")" 2>/dev/null || true
