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

# Walkthrough 2026-11-13 §9 item 2 (the relayed-proposal half): a real
# daemon dispatches a task to a fake agent (the 2026-11-11 durable-tasks
# walkthrough's fake-agy.py, reached through the real in-sandbox MCP
# server), which proposes what the pipeline steward files from a red
# candidate: an `[rc-blocker v1.1.0-rc.2]` revert with no found_in, one
# stating found_in, one stating a value that is no release, and one
# against no release. Prints each proposal's foundIn and task_link rows.
# Reuses ../2026-11-11-durable-tasks/{env,common}.sh as they are.
#
#   bash item2-found-in.sh [work-dir]
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
durable="$here/../2026-11-11-durable-tasks"
export DAEMON_PORT="${DAEMON_PORT:-4380}" GHSIM_PORT="${GHSIM_PORT:-4384}"
# shellcheck source=/dev/null
source "$durable/env.sh"
init_env "${1:-/tmp/s9-found}"
start_ghsim
start_daemon "$GRAIN" "$WORK_DIR"
# shellcheck source=/dev/null
source "$durable/common.sh"

T=$(api POST /api/tasks '{"title":"Triage the red v1.1.0-rc.2","description":"walkthrough: propose the rc blockers.","repo":"acme/widgets","approve":true}' | jq -r .id)
api POST "/api/tasks/$T/approve" '{}' >/dev/null || true
cat >"$WORK_DIR/action-$T.json" <<'JSON'
{"kind":"mcp_sequence","calls":[
 {"tool":"propose_task","args":{"title":"[rc-blocker v1.1.0-rc.2] Revert the scheduler change","body":"it broke qualification","at_front":true}},
 {"tool":"propose_task","args":{"title":"[rc-blocker v1.1.0-rc.2] Fix the flaky import","body":"rc.1 had it too","found_in":"v1.1.0-rc.1"}},
 {"tool":"propose_task","args":{"title":"Fix the crash on upgrade","body":"seen on the released build","found_in":"1.0.0"}},
 {"tool":"propose_task","args":{"title":"Fix the misnamed release","body":"some bug","found_in":"the latest one"}},
 {"tool":"propose_task","args":{"title":"Tidy the README","body":"no release"}},
 {"tool":"comment_on_issue","args":{"comment":"filed the rc blockers"}}]}
JSON
sleep 3
wait_rest "$T" || true
echo "task $T: $(api GET "/api/tasks/$T" | jq -r .state)"
echo "proposals (GET /api/tasks/{id} .foundIn):"
for id in $(api GET "/api/tasks?limit=50" | jq -r --arg t "$T" '.[] | select(.id != $t and (.title | test("Director") | not)) | .id' | sort -n); do
  api GET "/api/tasks/$id" | jq -r '"  #\(.id) \(.state) foundIn=\(.foundIn // "-") \(.title)"'
done
echo "task_link rows of kind found-in:"
sqlite3 "$WORK_DIR/data/store/grain.db" "SELECT task_id, target FROM task_link WHERE kind = 'found-in' ORDER BY task_id" | sed 's/^/  /'
echo "note left on the bad value:"
for id in $(api GET "/api/tasks?limit=50" | jq -r '.[] | select(.title == "Fix the misnamed release") | .id'); do
  api GET "/api/tasks/$id" | jq -r '.description // .body' | grep -o '_grain could not record.*' | sed 's/^/  /'
done
stop_env "$WORK_DIR"
