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

# §4 item 5: the cross-task rail on brief/compact/rollover, and what a
# wait_for_task result tells a waiter about a target in a repository its
# task cannot read.
set -uo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
set +e
echo '{"kind":"plain_reply","response":"idle"}' >"$WORK_DIR/action-default.json"
IDLE='{"kind":"resume_later","mcp_args":{"reason":"walk","when":{"at":"1h"}}}'
WRITE='{"grants":["https://grain.dev/capabilities/api#tasks:read","https://grain.dev/capabilities/api#tasks:write"]}'

# Targets: one in acme/other (outside A's repository), one beside A.
OTHER=$(api POST /api/tasks '{"title":"rail target in acme/other","description":"x","repo":"acme/other","approve":false}' | jq -r .id)
SAME=$(new_task "rail target in acme/widgets" "$IDLE" "$WRITE")
wait_state "$SAME" deferred
# Closed targets carrying a closing note, for wait_for_task.
OTHER_DONE=$(api POST /api/tasks '{"title":"closed target in acme/other","description":"x","repo":"acme/other","approve":false}' | jq -r .id)
api POST "/api/tasks/$OTHER_DONE/comments" '{"body":"SECRET-OTHER-NOTE: acme/other internals"}' >/dev/null
api POST "/api/tasks/$OTHER_DONE/close" '{"reason":"SECRET-OTHER-NOTE: closed with acme/other internals"}' >/dev/null
SAME_DONE=$(new_task "closed target in acme/widgets" '{"kind":"comment_on_issue","comment":"VISIBLE-SAME-NOTE: widgets done"}')
wait_state "$SAME_DONE" awaiting_reply closed
api POST "/api/tasks/$SAME_DONE/close" '{"reason":"VISIBLE-SAME-NOTE closing"}' >/dev/null
echo "targets: other=$OTHER same=$SAME other_closed=$OTHER_DONE same_closed=$SAME_DONE"

cmds=""
for t in "$OTHER" "$SAME"; do
	cmds+="echo \"-- task $t\"; "
	cmds+="grain api POST /api/tasks/$t/brief --body '{\"brief\":\"hijacked brief\",\"reason\":\"rail probe\"}' 2>&1 | head -c 400; echo; "
	cmds+="grain api POST /api/tasks/$t/compact --body '{\"handover\":\"hijacked handover\"}' 2>&1 | head -c 400; echo; "
	cmds+="grain api POST /api/tasks/$t/rollover --body '{\"handover\":\"hijacked\",\"reason\":\"rail probe\"}' 2>&1 | head -c 400; echo; "
done
A=$(new_task "rail caller" "$(jq -nc --arg c "$cmds" --arg o "$OTHER_DONE" --arg s "$SAME_DONE" '{"kind":"mcp_sequence","calls":[
	{"tool":"run_command","args":{"command":$c,"description":"rail probes"}},
	{"tool":"wait_for_task","args":{"task_id":$o,"timeout_seconds":5}},
	{"tool":"wait_for_task","args":{"task_id":$s,"timeout_seconds":5}}]}')" "$WRITE")
wait_state "$A" awaiting_reply closed deferred queued >/dev/null
sleep 8
echo "== brief/compact/rollover from task $A (acme/widgets, tasks:write)"
tail -1 "$WORK_DIR/runs/mcp-$A.jsonl" | jq -r '.outputs[0].text'
echo "== wait_for_task on $OTHER_DONE (acme/other)"
tail -1 "$WORK_DIR/runs/mcp-$A.jsonl" | jq -r '.outputs[1].text'
echo "== wait_for_task on $SAME_DONE (acme/widgets)"
tail -1 "$WORK_DIR/runs/mcp-$A.jsonl" | jq -r '.outputs[2].text'
echo "== the targets afterwards"
for t in "$OTHER" "$SAME"; do api GET "/api/tasks/$t" | jq -c '{id, state, currentBrief: (.current_brief // .currentBrief)}'; done
