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

# Helpers shared by the §7 item scripts (docs/test-plans/2026-11-13.md):
# api() from env.sh against a running daemon, the simulator's control
# routes, and waits on a task's state.

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=docs/test-runs/2026-11-13-actions-ci-for-agents/env.sh
source "$HERE/env.sh"
: "${WORK_DIR:=/tmp/walk-actions}"
G=${GRAIN:-/home/debian/bin/grain}

# up: a fresh simulator and daemon over SQLite, acme/widgets on ci_mode: github.
up() {
	init_env "$WORK_DIR"
	start_ghsim "$WORK_DIR"
	start_daemon "$G" "$WORK_DIR" "$@"
	export GRAIN_SERVER=$DAEMON GRAIN_TOKEN=$CODE
	api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"github"}' >/dev/null
}

sim() { # sim <method> <path> [body]
	if [[ -n "${3:-}" ]]; then
		curl -sS -X "$1" -H 'Content-Type: application/json' -d "$3" "http://$GHSIM$2"
	else
		curl -sS -X "$1" "http://$GHSIM$2"
	fi
}

# wait_rest <task>: until the task has no run in flight.
wait_rest() {
	local t=$1 s
	for _ in $(seq 200); do
		s=$(api GET "/api/tasks/$t" | jq -r .state)
		case "$s" in deferred | closed | parked | awaiting_reply | blocked | proposed) sleep 1 && return 0 ;; esac
		sleep 0.3
	done
	echo "task $t never came to rest (state $s)" >&2
	return 1
}

# file_task <title> <action-json>: file and approve a task on acme/widgets
# whose first run the fake agent scripts with <action-json>; prints its id.
file_task() {
	local id
	id=$(api POST /api/tasks "{\"title\":\"$1\",\"body\":\"walkthrough\",\"repo\":\"acme/widgets\"}" | jq -r .id)
	echo "$2" >"$WORK_DIR/action-$id.json"
	api POST "/api/tasks/$id/approve" '{}' >/dev/null
	sleep 2
	wait_rest "$id" >&2
	echo "$id"
}

# run_once <task> <action-json>: script the task's next run and start it now.
run_once() {
	echo "$2" >"$WORK_DIR/action-$1.json"
	api POST "/api/tasks/$1/run-now" '{}' >/dev/null
	sleep 2
	wait_rest "$1"
}

# last_outputs <task>: the fake agent's MCP tool outputs from its latest run.
last_outputs() { tail -n1 "$WORK_DIR/runs/mcp-$1.jsonl" | jq -r '.outputs[] | "--- \(.tool) isError=\(.isError)\n\(.text)"'; }

# branch_of <task>, head_of <task>: the task's branch and its head in the
# bare repository the simulator serves.
branch_of() { api GET "/api/tasks/$1" | jq -r '.branch // .branchName // empty'; }
head_of() { git -C "$WORK_DIR/widgets.git" rev-parse "refs/heads/$(branch_of "$1")"; }

# SH <command>: a run_command call for an mcp_sequence action.
SH() { jq -nc --arg c "$1" '{tool:"run_command",args:{command:$c,description:"walkthrough step"}}'; }
PARK='{"tool":"resume_later","args":{"reason":"walkthrough park","when":{"at":"1h"}}}'
