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

# fake-agy.sh: stub Antigravity CLI for a real `grain daemon -host-sandboxes`
# dispatch. Saves the prompt grain wrote on stdin, runs $WALK_DIR/hook.py
# against the run's own grain-sandbox MCP server, then reports success.
set -u
for arg in "$@"; do
  case "$arg" in
    --help) exit 0 ;;
    --version) echo "fake agy (walkthrough)"; exit 0 ;;
    models) exit 0 ;;
  esac
done
WALK_DIR="${WALK_DIR:-$(cat /var/tmp/capgrants/current-walk-dir)}"
mkdir -p "$WALK_DIR/runs"
MCP_CFG="$HOME/.gemini/config/mcp_config.json"
TASK_ID=$(python3 -c 'import json,sys
a=json.load(open(sys.argv[1]))["mcpServers"]["grain-sandbox"]["args"]
print(a[a.index("-task")+1] if "-task" in a else "unknown")' "$MCP_CFG" 2>/dev/null || echo unknown)
timeout 2 cat >>"$WALK_DIR/runs/prompt-$TASK_ID.jsonl" 2>/dev/null || true
cp "$MCP_CFG" "$WALK_DIR/runs/mcp-config-$TASK_ID.json" 2>/dev/null || true
if [[ -f "$WALK_DIR/hook.py" && ! -f "$WALK_DIR/runs/hook-$TASK_ID.json" ]]; then
  python3 "$WALK_DIR/hook.py" "$MCP_CFG" "$TASK_ID" "$WALK_DIR/runs/hook-$TASK_ID.json" >>"$WALK_DIR/runs/hook-$TASK_ID.log" 2>&1 || true
fi
printf '%s\n' '{"event":"init","conversation_id":"fake-agy-walk","init":{"tools":[],"permission_mode":"always-proceed"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-walk","step_index":9,"step_type":"agent_response","state":"DONE","text_delta":"walkthrough run complete"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"fake-agy-walk","status":"SUCCESS","response":"walkthrough run complete","num_turns":1}}'
