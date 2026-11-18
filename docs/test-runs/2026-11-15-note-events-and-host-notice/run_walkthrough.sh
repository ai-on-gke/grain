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

# docs/test-plans/2026-11-15.md §7 item 1 and 2: every relay path of a run's
# closing note and of the four question kinds, on a real `grain daemon
# -sandbox-port-base=0` with the fake agent and GitHub sim of
# docs/test-runs/2026-11-13-grain-health-exit-codes/, with
# plc-chat-gateway -watch-sse posting to a stub chat platform.
#
#   STORE=sqlite|postgres ./run_walkthrough.sh
#
# Writes $WORK_DIR/captures/events-*.json (GET /api/events after each
# step) and $WORK_DIR/captures/chat.jsonl (what the chat platform got).

set -euo pipefail

ME=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-13-grain-health-exit-codes/env.sh
source "$ME/../2026-11-13-grain-health-exit-codes/env.sh"
GATEWAY=${GATEWAY:-/home/debian/bin/plc-chat-gateway}
CHAT_PORT=${CHAT_PORT:-4287}
GATEWAY_PORT=${GATEWAY_PORT:-4286}
STORE=${STORE:-sqlite}
export STORE

work="/tmp/walk-notes-${STORE}"
init_env "$work"
start_ghsim "$work"
start_daemon "$work"

# The stub chat platform: the only thing stubbed. It appends each message
# the gateway POSTs to captures/chat.jsonl.
python3 -B - "$CHAT_PORT" "$work/captures/chat.jsonl" >"$work/chatstub.log" 2>&1 <<'PY' &
import http.server, sys
port, out = int(sys.argv[1]), sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        with open(out, "ab") as f:
            f.write(body + b"\n")
        self.send_response(200)
        self.end_headers()
    def log_message(self, *a):
        pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
PY
echo $! >"$work/chatstub.pid"
for _ in $(seq 50); do
	curl -s -o /dev/null -X POST -d "{}" "http://127.0.0.1:${CHAT_PORT}/ready" && break
	sleep 0.1
done
: >"$work/captures/chat.jsonl"
"$GATEWAY" -listen "127.0.0.1:${GATEWAY_PORT}" -grain-url "$DAEMON" -grain-token "$CODE" \
	-default-space spaces/walk -outbound-url "http://127.0.0.1:${CHAT_PORT}/post" -watch-sse \
	>"$work/gateway.log" 2>&1 &
echo $! >"$work/gateway.pid"
cleanup() {
	kill "$(cat "$work/gateway.pid")" "$(cat "$work/chatstub.pid")" 2>/dev/null || true
	stop_env "$work"
}
trap cleanup EXIT

state_of() { api GET "/api/tasks/$1" | jq -r '.state'; }
wait_state() {
	local id=$1 want=$2
	for _ in $(seq 240); do
		local s
		s=$(state_of "$id")
		if [[ ",$want," == *",$s,"* ]]; then
			echo "$s"
			return 0
		fi
		sleep 0.5
	done
	echo "task $id never reached $want (state $(state_of "$id")); daemon log tail:" >&2
	tail -n 30 "$work/daemon.log" >&2
	return 1
}

snapshot() {
	api GET "/api/events?limit=500" >"$work/captures/events-$1.json"
}
events_for() {
	jq -c --arg t "$1" '[.events[] | select(.taskId == $t) | {type, state, questionSummary, commentId}]' "$work/captures/events-$2.json"
}

# run_case LABEL TITLE WANT ACTION: files the task, gives the fake agent
# ACTION under the task's real id (__ID__ in ACTION becomes that id),
# approves it and waits for a state in WANT. Sets ID.
run_case() {
	local label=$1 title=$2 want=$3 act=$4
	ID=$(api POST /api/tasks "$(jq -cn --arg t "$title" --arg l "$label" \
		'{title: $t, description: ("Walkthrough case " + $l + "."), repo: "acme/widgets"}')" | jq -r '.id')
	printf '%s\n' "${act//__ID__/$ID}" >"$work/action-$ID.json"
	if [[ $(state_of "$ID") == proposed ]]; then
		api POST "/api/tasks/$ID/approve" '{}' >/dev/null
	fi
	echo "($label) task $ID: $(wait_state "$ID" "$want")"
	snapshot "$label"
}

declare -A IDS

# (a) A branchless report task: comment_on_issue + summary, nothing pushed.
run_case a "Branchless report" awaiting_reply,closed,failed "$(cat "$ME/actions/a.json")"
IDS[a]=$ID

# (b) A run that pushes a branch, so a pull request opens beside the note.
run_case b "Note beside a pull request" awaiting_submit,pr_open,ci_running,ci_failed,ci_passed,awaiting_review,awaiting_reply,closed,failed "$(cat "$ME/actions/b.json")"
IDS[b]=$ID

# (c) A report-only sub-task: the parent proposes it; the child has no
# action file of its own, so it takes action-default.json.
cp "$ME/actions/c-child.json" "$work/action-default.json"
run_case c-parent "Parent of a report-only sub-task" closed,failed,awaiting_reply "$(cat "$ME/actions/c-parent.json")"
IDS[c-parent]=$ID
child=""
for _ in $(seq 60); do
	child=$(api GET "/api/tasks?limit=200" | jq -r '[(if type == "array" then . else .tasks end)[] | select(.title == "Report-only sub-task")][0].id // empty')
	[[ -n "$child" ]] && break
	sleep 0.5
done
echo "(c) child: ${child:-none}, filed $(state_of "$child")"
if [[ $(state_of "$child") == proposed ]]; then
	api POST "/api/tasks/$child/approve" '{}' >/dev/null
fi
echo "(c) child task $child: $(wait_state "$child" closed,awaiting_reply,failed)"
rm -f "$work/action-default.json"
snapshot c
IDS[c]=$child

run_case ask "ask_question" awaiting_reply,closed,failed "$(cat "$ME/actions/ask.json")"
IDS[ask]=$ID
run_case secret "request_secret" awaiting_reply,closed,failed "$(cat "$ME/actions/secret.json")"
IDS[secret]=$ID
run_case access "request_access" awaiting_reply,closed,failed "$(cat "$ME/actions/access.json")"
IDS[access]=$ID
# A chat message: the chat tool, which grain gives only to an interactive
# run. The run blocks in chat until somebody replies; the events are
# snapshotted while it waits, then a reply lets it finish.
ID=$(api POST /api/tasks "$(jq -cn '{title: "chat message", description: "Walkthrough case chat.", repo: "acme/widgets", interactive: true}')" | jq -r '.id')
cp "$ME/actions/chat.json" "$work/action-$ID.json"
if [[ $(state_of "$ID") == proposed ]]; then
	api POST "/api/tasks/$ID/approve" '{}' >/dev/null
fi
for _ in $(seq 120); do
	api GET "/api/tasks/$ID/messages?after=0" 2>/dev/null | grep -q 'Which branch' && break
	sleep 0.5
done
sleep 1
echo "(chat) task $ID while chat waits: $(state_of "$ID")"
snapshot chat-waiting
api POST "/api/tasks/$ID/comments" '{"body": "main, please"}' >/dev/null
echo "(chat) task $ID after the reply: $(wait_state "$ID" awaiting_reply,closed,failed,idle,resting)"
snapshot chat
IDS[chat]=$ID

sleep 3
snapshot all
api GET "/api/events?wait=1s&since=0&limit=500" >"$work/captures/events-longpoll.json" || true
timeout 3 curl -sN -H "Authorization: Bearer $CODE" -H "Accept: text/event-stream" "$DAEMON/api/events?since=0" \
	>"$work/captures/events-sse.txt" || true
for k in a b c-parent c ask secret access chat; do
	echo "events ($k) task ${IDS[$k]}: $(events_for "${IDS[$k]}" all)"
done
for k in a b c ask secret access chat; do
	echo "row ($k): $(api GET "/api/tasks/${IDS[$k]}" | jq -c '{id, state, noteSummary, questionSummary, prUrl}')"
done
printf '%s\n' "${IDS[@]}" >"$work/captures/ids.txt"
for k in "${!IDS[@]}"; do echo "$k=${IDS[$k]}"; done >"$work/captures/ids.env"
sleep 6
echo "--- chat platform got:"
jq -c '{task_id, event_type, text}' "$work/captures/chat.jsonl"
echo "--- parked-on-a-question messages per task:"
grep -h 'parked on a question' "$work/captures/chat.jsonl" | jq -r '.task_id' | sort | uniq -c || true
if [[ -n ${KEEP:-} ]]; then
	trap - EXIT
	echo "KEEP set: daemon left running at $DAEMON (work $work)"
fi
