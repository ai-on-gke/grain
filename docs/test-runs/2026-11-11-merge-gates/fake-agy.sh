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

# fake-agy.sh: stub Antigravity CLI for a real `grain daemon -host-sandboxes
# -sandbox-port-base=0` dispatch (stubs only the model provider). Reads the
# prompt JSONL from stdin, runs hook_merge_gates.py against the run's own
# grain-sandbox MCP server over stdio, and streams a completed turn back.
set -u
for arg in "$@"; do
  case "$arg" in
    --help) exit 0 ;;
    --version) echo "fake agy (merge-gates walkthrough)"; exit 0 ;;
    models) exit 0 ;;
  esac
done
WALK_DIR="${WALK_DIR:-$(cat /var/tmp/mergegates/current-walk-dir)}"
mkdir -p "$WALK_DIR/runs"
MCP_CFG="$HOME/.gemini/config/mcp_config.json"
TASK_ID=$(PYTHONDONTWRITEBYTECODE=1 python3 -B -c 'import json,sys
a=json.load(open(sys.argv[1]))["mcpServers"]["grain-sandbox"]["args"]
print(a[a.index("-task")+1] if "-task" in a else "unknown")' "$MCP_CFG" 2>/dev/null || echo unknown)
PROMPT_FILE="$WALK_DIR/runs/prompt-$TASK_ID.jsonl"
timeout 2 cat >>"$PROMPT_FILE" 2>/dev/null || true
cp "$MCP_CFG" "$WALK_DIR/runs/mcp-config-$TASK_ID.json" 2>/dev/null || true
if [[ -f "$WALK_DIR/hook.py" && ! -f "$WALK_DIR/runs/hook-$TASK_ID.json" ]]; then
  PYTHONDONTWRITEBYTECODE=1 WALK_DIR="$WALK_DIR" python3 -B "$WALK_DIR/hook.py" \
    "$MCP_CFG" "$TASK_ID" "$PROMPT_FILE" "$WALK_DIR/runs/hook-$TASK_ID.json" \
    >>"$WALK_DIR/runs/hook-$TASK_ID.log" 2>&1 || true
fi
printf '%s\n' '{"event":"init","conversation_id":"fake-agy-walk","init":{"tools":[],"permission_mode":"always-proceed"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-walk","step_index":1,"step_type":"agent_response","state":"DONE","text_delta":"walkthrough run complete"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"fake-agy-walk","status":"SUCCESS","response":"walkthrough run complete","num_turns":1}}'
