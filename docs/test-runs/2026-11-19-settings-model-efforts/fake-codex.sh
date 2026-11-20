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

if [[ "${1:-}" == "--version" ]]; then
  echo "codex-cli 0.169.0"
  exit 0
fi

if [[ "${1:-}" == "debug" && "${2:-}" == "models" ]]; then
  printf '%s\n' '{"models":[{"slug":"gpt-5.1-codex","apply_patch_tool_type":null},{"slug":"gpt-5.1-codex-max","apply_patch_tool_type":null}]}'
  exit 0
fi

CONFIG_TOML=""
EFFORT_LINE=""
if [[ -n "${CODEX_HOME:-}" && -f "${CODEX_HOME}/config.toml" ]]; then
  CONFIG_TOML="$(cat "${CODEX_HOME}/config.toml")"
  EFFORT_LINE="$(grep -E '^model_reasoning_effort\s*=' "${CODEX_HOME}/config.toml" || true)"
fi

jq -nc \
  --arg tool "codex" \
  --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg codex_home "${CODEX_HOME:-}" \
  --arg effort_line "$EFFORT_LINE" \
  --arg config_toml "$CONFIG_TOML" \
  --args '{tool: $tool, ts: $ts, codexHome: $codex_home, effortLine: $effort_line, configToml: $config_toml, argv: $ARGS.positional}' \
  -- "$@" >> "$LOG_FILE"

cat >/dev/null || true

printf '%s\n' '{"type":"thread.started","thread_id":"fake-codex-thread-1"}'
printf '%s\n' '{"type":"item.started","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox","tool":"comment_on_issue","arguments":{"comment":"fake codex run completed"}}}'
printf '%s\n' '{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox","tool":"comment_on_issue","status":"completed","result":"ok"}}'
printf '%s\n' '{"type":"item.completed","item":{"id":"i2","item_type":"agent_message","text":"fake codex run completed"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}'
