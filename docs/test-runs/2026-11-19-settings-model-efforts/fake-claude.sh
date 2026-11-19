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

jq -nc \
  --arg tool "claude" \
  --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --args '{tool: $tool, ts: $ts, argv: $ARGS.positional}' \
  -- "$@" >> "$LOG_FILE"

cat >/dev/null || true

printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"mcp__grain-sandbox__comment_on_issue","input":{"comment":"fake claude run completed"}}]}}'
printf '%s\n' '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}}'
printf '%s\n' '{"type":"result","result":"fake claude run completed"}'
