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

# §4 item 3: one blocker per blocker-hold kind, each with a dependent;
# then the /api/inbox items, `grain task list --stalled` and
# list_grain_tasks(stalled) from inside a run; then close every blocker
# and read all three again.
set -uo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
set +e
echo '{"kind":"plain_reply","response":"idle"}' >"$WORK_DIR/action-default.json"
IDLE='{"kind":"resume_later","mcp_args":{"reason":"walk","when":{"at":"1h"}}}'

dependent() { # dependent <blocker> <label>
	new_task "dependent of $2 ($1)" '{"kind":"nothing_needed","reason":"dependent ran"}' "{\"dependsOn\":[\"$1\"]}"
}

declare -A B
# failed: five consecutive failed runs (a non-quota crash each time),
# driven by fail_blocker.sh beforehand because each crashed run takes ~90s.
B[failed]=$(cat "$WORK_DIR/failed-blocker.id")
# proposed: never approved.
B[proposed]=$(NO_APPROVE=1 new_task "blocker proposed" "$IDLE")
# awaiting_reply: asked a question.
B[asking]=$(new_task "blocker asking" '{"kind":"mcp_sequence","calls":[{"tool":"ask_question","args":{"question":"Go?","summary":"Approve the widget rename?"}}]}')
wait_state "${B[asking]}" awaiting_reply
# paused by an operator (parked first so no run is in flight).
B[paused]=$(new_task "blocker paused" "$IDLE")
wait_state "${B[paused]}" deferred
api POST "/api/tasks/${B[paused]}/pause" '{"reason":"walk"}' >/dev/null
# deferred with no wake time.
B[deferred]=$(new_task "blocker deferred" "$IDLE")
wait_state "${B[deferred]}" deferred
api POST "/api/tasks/${B[deferred]}/defer" '{}' >/dev/null
# not a hold: deferred on a resume_later time wake (grain releases it).
B[timed]=$(new_task "blocker timed (control)" "$IDLE")
wait_state "${B[timed]}" deferred

for k in "${!B[@]}"; do
	echo "blocker $k = ${B[$k]} ($(state "${B[$k]}"))"
done
declare -A D
for k in "${!B[@]}"; do D[$k]=$(dependent "${B[$k]}" "$k"); done
sleep 2

observe() { # observe <label>: all three surfaces
	echo "---- $1: /api/inbox stalled items"
	api GET /api/inbox | jq -c '.items[] | select(.title|test("^dependent of")) | {kind, task, title, summary}'
	local O
	O=$(new_task "observer $1" '{"kind":"mcp_sequence","calls":[{"tool":"run_command","args":{"command":"grain task list --stalled 2>&1 | grep -i -E \"dependent|blocker|stalled|queued\" ; true","description":"stalled list"}},{"tool":"list_grain_tasks","args":{"stalled":true}}]}' '{"grants":["https://grain.dev/capabilities/api#tasks:read","https://grain.dev/capabilities/api#self-debug"]}')
	wait_state "$O" closed awaiting_reply >/dev/null
	sleep 1
	echo "---- $1: grain task list --stalled (sandbox CLI, task $O)"
	tail -1 "$WORK_DIR/runs/mcp-$O.jsonl" | jq -r '.outputs[0].text'
	echo "---- $1: list_grain_tasks(stalled)"
	tail -1 "$WORK_DIR/runs/mcp-$O.jsonl" | jq -r '.outputs[1].text'
}
observe "before"

echo "== closing every blocker"
for k in "${!B[@]}"; do api POST "/api/tasks/${B[$k]}/close" '{"reason":"walk"}' >/dev/null; done
sleep 4
for k in "${!D[@]}"; do echo "dependent $k ${D[$k]}: $(state "${D[$k]}")"; done
observe "after"
