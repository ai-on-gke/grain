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

# docs/test-plans/2026-11-14.md §4 item 3 (dispatch half), after
# walk_chain.mjs saved default walk-a with fallbacks walk-b, walk-c:
# disable conn-a (the only connection serving walk-a), hold conn-b's one
# slot with a task pinned to walk-b, then queue an unpinned task and a
# second walk-b-pinned one, and report the model each run got. Every fake
# agy run holds until $WORK_DIR/release exists.

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
set +e
CODE=$(tr -d '[:space:]' <"$WORK_DIR/data/local-api-token")
rm -f "$WORK_DIR/release"
cat >"$WORK_DIR/action-default.json" <<EOF
{"kind": "mcp_sequence", "calls": [
  {"tool": "run_command", "args": {"command": "while [ ! -f $WORK_DIR/release ]; do sleep 1; done; echo released", "description": "holding the connection", "timeout": 600000}},
  {"tool": "nothing_needed", "args": {"reason": "walkthrough run released"}}
]}
EOF

file_task() { api POST /api/tasks "$1" | jq -r .id; }
runs() {
	local id
	for id in "$@"; do
		api GET "/api/tasks/$id" | jq -c --arg id "$id" '{id: $id, state, model, pinned: .agentModel, runs: [.runs[]? | {model, agentBackendId, state}], waiting: (.waitingReason // .dispatchNote // .blockedReason)}'
	done
}

echo "-- disable conn-a"
api GET /api/settings | jq -c '.agentBackends[] | select(.id=="conn-a")' >"$WORK_DIR/conn-a.json"
api PUT /api/deployment/agent-backends/conn-a "$(jq -c '. + {disabled: true}' "$WORK_DIR/conn-a.json")" | jq -c '.agentBackends[]? | select(.id=="conn-a") | {id, disabled}'
api GET /api/deployment | jq -c '{defaultModels}'

hold=$(file_task '{"title":"Hold conn-b","description":"Pinned to walk-b; holds the only slot.","repo":"acme/widgets","approved":true,"model":"walk-b"}')
echo "-- filed $hold (pinned walk-b); waiting for it to start"
for _ in $(seq 60); do
	[[ $(api GET "/api/tasks/$hold" | jq -r .state) == running ]] && break
	sleep 1
done
unpinned=$(file_task '{"title":"Unpinned task","description":"Takes the deployment default chain.","repo":"acme/widgets","approved":true}')
pinned=$(file_task '{"title":"Pinned to walk-b","description":"Must wait for conn-b.","repo":"acme/widgets","approved":true,"model":"walk-b"}')
echo "-- filed $unpinned (unpinned) and $pinned (pinned walk-b); letting dispatch run"
sleep 20
runs "$hold" "$unpinned" "$pinned"
echo "$hold $unpinned $pinned" >"$WORK_DIR/dispatch.ids"
