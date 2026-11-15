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

# item5.sh <sqlite|postgres> [port]: §3 item 5, a comment left on a closed
# task is delivered to the next run exactly once.
#
# A no-repo task runs once under fake-agy.sh (which answers in prose, leaving
# it awaiting_reply) and is closed. Then, on the closed task:
#   A -- a plain comment, then a separate POST /reopen
#   B -- a comment posted with "reopen": true (the UI's "Reopen and send")
#   C -- a bare reopen with no new comment
# Each reopen's run saves its prompt (fake-agy.sh); the script counts each
# marker in every prompt. A and B must each appear in exactly one prompt,
# the one right after it was posted, and C's run must carry neither.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=common.sh
source "$HERE/common.sh"
kind=${1:-sqlite} port=${2:-4310}
W=/tmp/wl-item5-$kind
OUT=${OUT:-$HERE/out/item5-$kind}
mkdir -p "$OUT"
reset_store "$kind" "$W"
start_daemon "$kind" "$W" "$port" || exit 1
setup_agent

done_runs() { api GET "/api/tasks/$task" | jq '[.attempts[]? | select(.outcome != null and .outcome != "")] | length'; }
settle() { # settle <runs-wanted>: wait for that many finished runs
	for _ in $(seq 90); do
		[[ $(done_runs) -ge $1 ]] && { sleep 3; api GET "/api/tasks/$task" | jq -r '"  after run \(.attempts|length): state=\(.state) prompts saved=\('"$(ls "$W"/prompts/*.txt 2>/dev/null | wc -l)"')"'; return 0; }
		sleep 2
	done
	echo "  timed out waiting for run $1"; tail -n 5 "$W/daemon.log"; exit 1
}
state() { api GET "/api/tasks/$task" | jq -r '"  \(.state), \(.attempts|length) runs"'; }

{
	task=$(api POST /api/tasks '{"title":"reopen walk","body":"say done","noRepo":true}' | jq -r .id)
	api POST "/api/tasks/$task/approve" '{}' >/dev/null
	api POST "/api/tasks/$task/run-now" '{}' >/dev/null
	echo "task $task filed, approved, run now"
	settle 1
	api POST "/api/tasks/$task/close" '{}' >/dev/null
	echo "A: closed; comment, then a separate reopen"
	api POST "/api/tasks/$task/comments" '{"body":"WALK-MARKER-A please also check the README"}' >/dev/null
	sleep 3; state
	api POST "/api/tasks/$task/reopen" '{}' >/dev/null; state
	settle 2
	api POST "/api/tasks/$task/close" '{}' >/dev/null
	echo "B: closed; comment with reopen: true"
	api POST "/api/tasks/$task/comments" '{"body":"WALK-MARKER-B and the LICENSE","reopen":true}' >/dev/null; state
	settle 3
	api POST "/api/tasks/$task/close" '{}' >/dev/null
	echo "C: closed; bare reopen, no new comment"
	api POST "/api/tasks/$task/reopen" '{}' >/dev/null; state
	sleep 15; state
	echo "marker occurrences per saved prompt (file: A B):"
	for f in $(ls -tr "$W"/prompts/*.txt); do
		echo "  $(basename "$f"): $(grep -o WALK-MARKER-A "$f" | wc -l) $(grep -o WALK-MARKER-B "$f" | wc -l)"
	done
	echo "task events:"
	api GET "/api/events?since=0&limit=200" | jq -r --arg t "$task" '.events[] | select(.taskId == $t) | "  \(.id) \(.type) \(.previousState // "") -> \(.state // "")"'
} 2>&1 | tee "$OUT/summary.txt"
cp "$W"/prompts/*.txt "$OUT/" 2>/dev/null
stop_daemon
