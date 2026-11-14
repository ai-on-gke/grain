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
# Helpers shared by the item scripts: start the environment, file a task,
# wait for a condition.

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
OUT="$HERE/out"
mkdir -p "$OUT"

push_action() {
	cat <<'J'
{"kind":"mcp_sequence","calls":[
 {"tool":"run_command","args":{"command":"cd work && mkdir -p notes && echo 'change from task {TASK}' > notes/{TASK}.txt && git add -A && git commit -qm 'docs: note from task {TASK}' -m 'Walkthrough change.' && git push -q origin HEAD && git log --oneline -1","description":"pushing a one-file change"}},
 {"tool":"comment_on_issue","args":{"comment":"pushed a one-file change"}}
]}
J
}

clean_review_action() {
	echo '{"kind":"mcp_sequence","calls":[{"tool":"comment_on_issue","args":{"comment":"No findings. Checked the one-file diff."}}]}'
}

boot() {
	# One environment at a time: they share the ports.
	local d
	for d in /tmp/walk-merge*; do [[ -d $d ]] && stop_env "$d"; done
	init_env "${1:-/tmp/walk-merge}"
	push_action >"$WORK_DIR/action-default.json"
	clean_review_action >"$WORK_DIR/action-review.json"
	start_ghsim
	shift || true
	start_daemon "$GRAIN" "$WORK_DIR" "$@"
}

# file_task <title> [priority] -> prints the id; approved, auto-merge on.
file_task() {
	local title=$1 prio=${2:-5} id
	id=$(api POST /api/tasks "{\"title\":\"$title\",\"description\":\"$title.\",\"repo\":\"acme/widgets\",\"autoMerge\":true,\"priority\":$prio}" | jq -r .id)
	api POST "/api/tasks/$id/approve" >/dev/null
	echo "$id"
}

task() { api GET "/api/tasks/$1"; }

# wait_for <seconds> <shell condition>
wait_for() {
	local n=$1
	shift
	for _ in $(seq "$((n * 2))"); do
		if eval "$*"; then return 0; fi
		sleep 0.5
	done
	echo "timed out waiting for: $*" >&2
	return 1
}

tasks() { api GET '/api/tasks?limit=100' | jq -c '.[] | {id,title,state,priority}'; }
