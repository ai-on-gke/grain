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

# §4 item 4: wakes on a real daemon started with `-ci-wake-backstop 1m`
# (./up.sh -ci-wake-backstop 1m), on grain CI (acme/widgets'
# .grain/workflows/ci.yml; a commit carrying slow.txt never reports).
set -uo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
set +e
echo '{"kind":"plain_reply","response":"idle"}' >"$WORK_DIR/action-default.json"
push() { # push <file>: commit <file> on the task branch and push it
	echo "cd work && date > $1 && git add $1 && git -c user.name=walk -c user.email=walk@example.com commit -qm 'chore: $1' && git push -q origin HEAD 2>&1 | tail -2; git log -1 --format=%h"
}
runs() { tail -n +1 "$WORK_DIR/runs/task-$1.jsonl" 2>/dev/null | wc -l; }
since() { echo $(($(date +%s) - $1)); }

echo "== 4a: resume_later on CI, and the run then ends in an error"
A=$(new_task "ci wait then error" "$(jq -nc --arg c "$(push walk.txt)" '{"kind":"mcp_sequence","calls":[
	{"tool":"run_command","args":{"command":$c,"description":"push"}},
	{"tool":"resume_later","args":{"reason":"wait for CI","when":{"ci":"my-branch"}}}],
	"exit_error":"RESOURCE_EXHAUSTED: quota exhausted after resume_later"}')")
t0=$(date +%s)
wait_state "$A" deferred
echo "   deferred after $(since "$t0")s; first run said: $(tail -1 "$WORK_DIR/runs/mcp-$A.jsonl" | jq -r '.outputs[1].text' | head -1)"
script_next "$A" '{"kind":"nothing_needed","reason":"woke on the CI verdict"}'
wait_state "$A" closed queued running
echo "   woke after $(since "$t0")s (state $(state "$A")); runs so far: $(runs "$A")"
api GET "/api/ci/runs?repo=acme/widgets&limit=3" | jq -c '.[]? | {id, ref: (.ref // .branch), conclusion, status}' 2>/dev/null | head -3

echo "== 4b: resume_later on CI that never reports, backstop 1m"
B=$(new_task "ci wait never reports" "$(jq -nc --arg c "$(push slow.txt)" '{"kind":"mcp_sequence","calls":[
	{"tool":"run_command","args":{"command":$c,"description":"push"}},
	{"tool":"resume_later","args":{"reason":"wait for CI","when":{"ci":"my-branch"}}}]}')")
t0=$(date +%s)
wait_state "$B" deferred
script_next "$B" '{"kind":"nothing_needed","reason":"woke at the backstop"}'
for _ in $(seq 60); do [[ $(state "$B") != deferred ]] && break; sleep 3; done
echo "   undeferred after $(since "$t0")s (state $(state "$B"))"
api GET "/api/tasks/$B" | jq -r '.comments[] | select(.body|test("backstop")) | .body' | head -c 400
echo
api GET /api/notifications | jq -c ".[] | select(.taskId==\"$B\") | {family, title}"

echo "== 4c: resume_later with message:true wakes on a person's comment and on a relayed one"
C=$(new_task "message wait" '{"kind":"resume_later","mcp_args":{"reason":"wait for a person","when":{"message":true}}}')
wait_state "$C" deferred
script_next "$C" '{"kind":"resume_later","mcp_args":{"reason":"wait again","when":{"message":true}}}'
t0=$(date +%s)
as_person alice@example.com POST "/api/tasks/$C/comments" '{"body":"alice: go ahead"}' >/dev/null
wait_state "$C" queued running closed
echo "   person's comment: woke after $(since "$t0")s"
wait_state "$C" deferred
script_next "$C" '{"kind":"nothing_needed","reason":"woke on the relayed answer"}'
S=$(as_person alice@example.com POST /api/console-sessions '{"title":"alice wakes"}' | jq -r '.sessions[] | select(.title=="alice wakes") | .taskId')
script_next "$S" "$(jq -nc --arg c "grain task message $C --relay 'relayed: go ahead'; echo rc=\$?" '{"kind":"mcp_sequence","calls":[{"tool":"run_command","args":{"command":$c,"description":"relay"}}]}')"
t0=$(date +%s)
as_person alice@example.com POST "/api/tasks/$S/comments" '{"body":"tell the waiting task to go ahead"}' >/dev/null
wait_state "$C" queued running closed
echo "   relayed answer: woke after $(since "$t0")s; relay said: $(tail -1 "$WORK_DIR/runs/mcp-$S.jsonl" | jq -r '.outputs[-1].text' | grep -v '^$' | tr '\n' ' ')"
wait_state "$C" closed

echo "== 4d: a park on task P resumes when P closes and leaves no depends-on link"
P=$(new_task "park target" '{"kind":"resume_later","mcp_args":{"reason":"hold","when":{"at":"1h"}}}')
wait_state "$P" deferred
W=$(new_task "parker" "$(jq -nc --arg p "$P" '{"kind":"mcp_sequence","calls":[{"tool":"wait_for_task","args":{"task_id":$p,"park":true}}]}')")
wait_state "$W" queued deferred
sleep 3
echo "   parked: state $(state "$W"), blocked $(api GET "/api/tasks/$W" | jq .blocked)"
"$G" task deps "$W" 2>/dev/null | head -5
api GET "/api/tasks/$W" | jq -c '{dependsOn}'
script_next "$W" '{"kind":"nothing_needed","reason":"resumed after the park"}'
t0=$(date +%s)
api POST "/api/tasks/$P/close" '{"reason":"done"}' >/dev/null
wait_state "$W" closed
echo "   resumed and closed $(since "$t0")s after P closed"
api POST "/api/tasks/$P/reopen" '{}' >/dev/null
sleep 2
echo "   P reopened: $(state "$P"); W's links now: $(api GET "/api/tasks/$W" | jq -c '{dependsOn, blocked}')"
grep -c "parked on\|waited on" "$(ls -t "$WORK_DIR"/runs/prompt-"$W"-*.txt | head -1)" | sed 's/^/   resumed prompt mentions the park: /'
