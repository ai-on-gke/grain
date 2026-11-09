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

# fake-agy.sh: stub antigravity CLI for real `grain daemon -host-sandboxes`
# runs in the 2026-11-08 §3 walkthrough.
for arg in "$@"; do
  case "$arg" in
    --version) echo 'fake agy (walk)'; exit 0 ;;
    models) exit 0 ;;
  esac
done
IFS= read -r _ || true
WALK_DIR=${WALK_DIR:-/tmp/walk-slice2}
mkdir -p "$WALK_DIR/runs"
log="$WALK_DIR/runs/run-$$.log"
{
  echo "== $(date -u +%FT%TZ) fake-agy pwd=$(pwd) HOME=$HOME"
  ls -la "$HOME" 2>&1
} >>"$log" 2>&1
if [ -f "$WALK_DIR/step.sh" ]; then
  sh "$WALK_DIR/step.sh" >>"$log" 2>&1 || true
fi
printf '%s\n' '{"event":"init","conversation_id":"fake-agy-walk","init":{"tools":[],"permission_mode":"always-proceed"}}'
if [ -f "$WALK_DIR/stream.jsonl" ]; then
  cat "$WALK_DIR/stream.jsonl"
  rm -f "$WALK_DIR/stream.jsonl"
fi
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-walk","step_index":90,"step_type":"agent_response","state":"ACTIVE","text_delta":"walk step done"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-walk","step_index":90,"step_type":"agent_response","state":"DONE"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"fake-agy-walk","status":"SUCCESS","response":"walk step done","num_turns":1}}'
