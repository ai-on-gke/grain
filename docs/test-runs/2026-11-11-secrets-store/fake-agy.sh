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
# dispatch. Answers the probes at once; on a real run it runs hook.py, which
# calls the run's own grain-sandbox MCP server over stdio and writes what it
# saw to $WALK_DIR/runs/<task>.json. Then it emits a valid agy stream-json
# completion.
set -u
for arg in "$@"; do
	case "$arg" in
	--help) exit 0 ;;
	--version) echo "fake agy (secrets walkthrough)"; exit 0 ;;
	models) exit 0 ;;
	esac
done
read -t 0.2 -r _ >/dev/null 2>&1 || true
WALK_DIR="${WALK_DIR:-/var/tmp/ss/walk/walk}"
mkdir -p "$WALK_DIR/runs"
python3 "$(dirname "$0")/hook.py" "$HOME/.gemini/config/mcp_config.json" "$WALK_DIR" >>"$WALK_DIR/runs/hook.log" 2>&1 || true
printf '%s\n' '{"event":"init","conversation_id":"fake-agy-walk","init":{"tools":[],"permission_mode":"always-proceed"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-walk","step_index":99,"step_type":"agent_response","state":"ACTIVE","text_delta":"walkthrough run complete"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-walk","step_index":99,"step_type":"agent_response","state":"DONE"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"fake-agy-walk","status":"SUCCESS","response":"walkthrough run complete","num_turns":1}}'
