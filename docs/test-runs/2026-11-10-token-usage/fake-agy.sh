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

# fake-agy: the stub antigravity CLI for §1's real daemons (no model
# credentials exist in the sandbox, so every framework here is a fake).
# Each exec prints agy's stream-json for a one-turn run whose `result`
# carries a usage block (the shape pkg/agent/antigravity's parser reads,
# docs/agy-surface.md) and appends what it reported to
# $WALK_DIR/emitted.log: "<seq> <pid> <mode> input output thinking
# cache_read" -- the CLI's own report the store is checked against.
#
# $WALK_DIR/mode-<task> (the task is read off --add-dir .../runs/<task>-<n>),
# else $WALK_DIR/mode, picks what the exec does:
#   ok        report usage and answer (default)
#   slow:N    the same after sleeping N seconds (concurrency, item 4)
#   fail      exit 1 before any model call: no result event, no usage
#   refuse    a usage-limit refusal ("Individual quota reached ... Resets
#             in 1h0m0s") with no usage block (item 3)
for arg in "$@"; do
  case "$arg" in --help) exit 0;; --version) echo 'fake agy (token-usage walk)'; exit 0;; models) exit 0;; esac
done
IFS= read -r _ || true
W=${WALK_DIR:-/home/debian/walk}
task=$(printf '%s\n' "$@" | sed -n 's|.*/runs/\([0-9]*\)-[0-9]*$|\1|p' | head -n 1)
mode=$(cat "$W/mode-$task" 2>/dev/null || cat "$W/mode" 2>/dev/null || echo ok)
# One sequence number per exec, under a lock.
seq=$(flock "$W/seq.lock" sh -c 'n=$(( $(cat '"$W"'/seq 2>/dev/null || echo 0) + 1 )); echo $n > '"$W"'/seq; echo $n')
echo "$(date -u +%FT%TZ) exec seq=$seq task=$task mode=$mode args=$*" >> "$W/runs/execs.log"
case "$mode" in
  fail) echo "fake agy: cannot reach the model (walk: fail mode)" >&2; exit 1;;
  slow:*) sleep "${mode#slow:}";;
  refuse)
    echo "$seq task=$task refuse 0 0 0 0" >> "$W/emitted.log"
    printf '%s\n' '{"event":"init","conversation_id":"fake-agy-'"$seq"'","init":{"tools":[],"permission_mode":"always-proceed"}}'
    printf '%s\n' '{"event":"result","result":{"conversation_id":"fake-agy-'"$seq"'","status":"ERROR","error":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h0m0s.","response":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h0m0s.","num_turns":0}}'
    exit 1;;
esac
in=$((10000 + seq * 1000)); out=$((200 + seq * 10)); th=50; cr=4000
echo "$seq task=$task $mode $in $out $th $cr" >> "$W/emitted.log"
printf '%s\n' '{"event":"init","conversation_id":"fake-agy-'"$seq"'","init":{"tools":[],"permission_mode":"always-proceed"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-'"$seq"'","step_index":0,"step_type":"agent_response","state":"ACTIVE","text_delta":"walk run done."}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-'"$seq"'","step_index":0,"step_type":"agent_response","state":"DONE"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"fake-agy-'"$seq"'","status":"SUCCESS","response":"walk run done.","num_turns":1,"usage":{"input_tokens":'$in',"output_tokens":'$out',"thinking_tokens":'$th',"cache_read_tokens":'$cr',"total_tokens":'$((in+out+th))'}}}'
