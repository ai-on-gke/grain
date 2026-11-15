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

# fake-agy.sh: the stub antigravity CLI for §3's real daemons (no model
# credentials exist in the sandbox). Each exec saves the whole prompt it was
# given on stdin to $WALK_DIR/prompts/<task>-<seq>.txt -- item 5 reads the
# reopened run's prompt from there -- and prints agy's stream-json for a
# one-turn run that answers in prose and makes no tool call.
for arg in "$@"; do
	case "$arg" in --help) exit 0 ;; --version) echo 'fake agy (live-events walk)'; exit 0 ;; models) exit 0 ;; esac
done
W=${WALK_DIR:-/tmp/walk-live}
mkdir -p "$W/prompts"
task=$(printf '%s\n' "$@" | sed -n 's|.*/runs/\([0-9]*\)-[0-9]*$|\1|p' | head -n 1)
seq=$(flock "$W/prompts/.lock" sh -c 'n=$(( $(cat '"$W"'/prompts/.seq 2>/dev/null || echo 0) + 1 )); echo $n > '"$W"'/prompts/.seq; echo $n')
# The prompt arrives on stdin (or as the last argument on older agy shapes).
timeout 5 cat >"$W/prompts/${task:-x}-$seq.txt" || true
printf '%s\n' "$@" >"$W/prompts/${task:-x}-$seq.args"
printf '%s\n' '{"event":"init","conversation_id":"fake-agy-'"$seq"'","init":{"tools":[],"permission_mode":"always-proceed"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-'"$seq"'","step_index":0,"step_type":"agent_response","state":"ACTIVE","text_delta":"walk run done."}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-'"$seq"'","step_index":0,"step_type":"agent_response","state":"DONE"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"fake-agy-'"$seq"'","status":"SUCCESS","response":"walk run done.","num_turns":1,"usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}'
