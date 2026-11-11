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

# fake-agy: the stub framework for a real `grain daemon` -- an antigravity
# connection whose CLI path is this file (tests/e2e/fake_agy_test.go's
# writeFakeAgy, as a script). It answers --version and `models`, and for a
# run it records its environment and home, then runs the walk's current
# probe ($WALK_DIR/step.sh: step-model-proxy.sh, step-server-drop.sh), then
# prints agy's stream-json for a successful one-turn run. The agent CLI runs
# on grain's host even when the run's sandbox is a container, so a probe
# looks into the sandbox with docker exec or through the run's own grain
# MCP server (mcp-call.py). Everything it records goes to
# $WALK_DIR/runs/task-<id>.log (task-unknown.log: grain sets no
# GRAIN_TASK_ID for the CLI); stdout is the stream grain parses.
#
# 2026-11-08: one addition to 2026-11-04-capabilities/fake-agy.sh. A probe
# that calls grain's MCP tools itself (mcp-propose.py) appends agy's own
# step_update events for those calls to $WALK_DIR/stream.jsonl, and they are
# printed ahead of the closing turn: grain reads a run's propose_task calls
# from the agent's stream (result.ToolCalls, with the arguments the MCP
# server recorded), so a call the stream never reports is never filed.
for arg in "$@"; do
  case "$arg" in --version) echo 'fake agy (walk)'; exit 0;; models) exit 0;; esac
done
IFS= read -r _ || true
WALK_DIR=${WALK_DIR:-/home/debian/walk}
task=$(printf '%s' "${GRAIN_TASK_ID:-unknown}" | tr -cd 'A-Za-z0-9_-')
log="$WALK_DIR/runs/task-$task.log"
{
  echo "== $(date -u +%FT%TZ) fake agy run, pwd=$(pwd) HOME=$HOME"
  echo "== args: $*"
  echo "== env (GRAIN_*, *_PROXY*, GOOGLE_*, CLOUDSDK_*; values of secrets cut)"
  env | grep -E '^(GRAIN_|GOOGLE_|CLOUDSDK_|[A-Z_]*BASE_URL|[A-Z_]*PROXY)' | sed -E 's/((TOKEN|KEY|SECRET)[A-Z_]*=).{6}.*/\1<cut>/' | sort
  echo "== ls -la \$HOME"
  ls -la "$HOME" 2>&1
} >> "$log" 2>&1
step="$WALK_DIR/step-$task.sh"; [ -f "$step" ] || step="$WALK_DIR/step.sh"
if [ -f "$step" ]; then
  sh "$step" >> "$log" 2>&1
fi
echo "== $(date -u +%FT%TZ) done" >> "$log"
printf '%s\n' '{"event":"init","conversation_id":"fake-agy-walk","init":{"tools":[],"permission_mode":"always-proceed"}}'
if [ -f "$WALK_DIR/stream.jsonl" ]; then cat "$WALK_DIR/stream.jsonl"; rm -f "$WALK_DIR/stream.jsonl"; fi
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-walk","step_index":90,"step_type":"agent_response","state":"ACTIVE","text_delta":"walk step done"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-walk","step_index":90,"step_type":"agent_response","state":"DONE"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"fake-agy-walk","status":"SUCCESS","response":"walk step done","num_turns":1}}'
