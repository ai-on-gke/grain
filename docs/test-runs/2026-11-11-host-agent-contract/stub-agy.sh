#!/bin/sh
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

# stub-agy.sh: stands in for the agy CLI on walkhost (only the model
# provider is stubbed). It records its argv and environment, then does what
# agy does with grain's hooks: reads $HOME/.gemini/config/hooks.json, waits
# (so the daemon can be restarted mid-run), and forks the PreToolUse hook
# command for a native tool grain refuses (run_command) and for one of
# grain's own MCP tools, recording each verdict.
case "${1:-}" in
--version|-v|version) echo "0.0.0 (walkthrough stub agy)"; exit 0 ;;
esac
log=/home/walkhost/stub-logs
mkdir -p "$log"
out="$log/agy.$$.txt"
{
	echo "argv: $*"
	echo "cwd: $(pwd)"
	echo "--- env"
	env | sort
} >"$out"
echo '{"event":"init","init":{"cwd":"/w","tools":["run_command"],"permission_mode":"bypass"}}'
sleep "${STUB_HOOK_DELAY:-0}"
[ -f /home/walkhost/stub-hook-delay ] && sleep "$(cat /home/walkhost/stub-hook-delay)"
hooks="$HOME/.gemini/config/hooks.json"
if [ -f "$hooks" ]; then
	echo "--- hooks.json" >>"$out"
	cat "$hooks" >>"$out"
	cmd=$(python3 -c 'import json,sys
d=json.load(open(sys.argv[1]))
for v in d.values():
    for m in v.get("PreToolUse",[]):
        for h in m.get("hooks",[]):
            print(h["command"]); sys.exit(0)' "$hooks")
	for tool in run_command mcp_grain-sandbox_ask_question; do
		verdict=$(printf '{"toolCall":{"name":"%s","args":{}}}' "$tool" | sh -c "$cmd" 2>>"$out")
		echo "hook $tool => ${verdict:-<no opinion>} (exit $?)" >>"$out"
	done
fi
echo '{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","output":"ok"}}}'
echo '{"event":"result","result":{"status":"SUCCESS","response":"stub agy done."}}'
echo "--- done" >>"$out"
