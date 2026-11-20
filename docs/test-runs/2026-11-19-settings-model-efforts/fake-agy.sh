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

set -euo pipefail

LOG_FILE="${WALK_LOG_DIR:?WALK_LOG_DIR must be set}/invocations.jsonl"
mkdir -p "$(dirname "$LOG_FILE")"

# Record exact argv as JSON.
jq -nc \
  --arg tool "agy" \
  --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg home "${HOME:-}" \
  --args '{tool: $tool, ts: $ts, home: $home, argv: $ARGS.positional}' \
  -- "$@" >> "$LOG_FILE"

if [[ "${1:-}" == "--help" ]]; then
  cat <<'EOF'
Usage: agy [options]
  --model <model>   Model to run
  --effort <level>  Reasoning effort for the current CLI session (low|medium|high|xhigh|max)
EOF
  exit 0
fi

if [[ "${1:-}" == "models" ]]; then
  printf 'gemini-3.1-pro\tGemini 3.1 Pro\n'
  printf 'opus-5.5-high\tOpus 5.5 (High)\n'
  printf 'opus-5.5-xhigh\tOpus 5.5 (XHigh)\n'
  printf 'gemini-4-carbon-xhigh\tGemini 4 Carbon (XHigh)\n'
  printf 'auto\tAuto\n'
  exit 0
fi

# Drain prompt from stdin without failing on empty input.
cat >/dev/null || true

printf '%s\n' '{"event":"init","init":{"cwd":"/tmp","tools":["mcp_grain-sandbox_comment_on_issue"],"permission_mode":"bypass"}}'
printf '%s\n' '{"event":"step_update","step_update":{"step_index":0,"state":"ACTIVE","step_type":"tool","tool_name":"mcp_grain-sandbox_comment_on_issue","tool_info":{"name":"mcp_grain-sandbox_comment_on_issue","parameters":{"comment":"fake agy run completed"}}}}'
printf '%s\n' '{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"mcp_grain-sandbox_comment_on_issue","tool_info":{"name":"mcp_grain-sandbox_comment_on_issue","output":"ok"}}}'
printf '%s\n' '{"event":"result","result":{"status":"SUCCESS","response":"fake agy run completed"}}'
