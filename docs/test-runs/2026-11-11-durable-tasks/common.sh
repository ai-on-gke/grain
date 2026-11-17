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

# Helpers shared by the item scripts: api() from env.sh against an
# already-running daemon, and waits on a task's state.
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
source "$HERE/env.sh"
: "${WORK_DIR:=/tmp/walk-durable}"
DAEMON=${DAEMON:-http://127.0.0.1:${DAEMON_PORT:-4280}}
CODE=$(tr -d '[:space:]' <"$WORK_DIR/data/local-api-token")
export WORK_DIR DAEMON CODE GRAIN_SERVER=$DAEMON GRAIN_TOKEN=$CODE
G=${GRAIN:-/home/debian/bin/grain}
PARK='{"kind":"resume_later","mcp_args":{"reason":"walkthrough park","when":{"at":"1h"}}}'

# wait_rest <task>: until the task has no run in flight.
wait_rest() {
	local t=$1 s
	for _ in $(seq 150); do
		s=$(api GET "/api/tasks/$t" | jq -r .state)
		case "$s" in deferred|closed|parked|awaiting_reply|blocked) [[ ! -e "$WORK_DIR/live-$t.hold" ]] && sleep 1.5 && return 0 ;; esac
		sleep 0.3
	done
	echo "task $t never came to rest (state $s)" >&2
	return 1
}

# run_once <task> <action-json>: script the next run and start it now.
run_once() {
	echo "$2" >"$WORK_DIR/action-$1.json"
	api POST "/api/tasks/$1/run-now" '{}' >/dev/null
	sleep 2
	wait_rest "$1"
}

latest_prompt() { ls -t "$WORK_DIR"/runs/prompt-"$1"-*.txt | grep -v -- "-$1-x" | head -1; }
