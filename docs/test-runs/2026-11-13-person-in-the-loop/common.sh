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

# Shared helpers for the item scripts, against a daemon up.sh started.
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
source "$HERE/env.sh"
: "${WORK_DIR:=/tmp/walk-pitl}"
DAEMON=${DAEMON:-http://127.0.0.1:${DAEMON_PORT:-4280}}
CODE=$(tr -d '[:space:]' <"$WORK_DIR/data/local-api-token")
export WORK_DIR DAEMON CODE GRAIN_SERVER=$DAEMON GRAIN_TOKEN=$CODE
G=${GRAIN:-/home/debian/bin/grain}

state() { api GET "/api/tasks/$1" | jq -r .state; }

# wait_state <task> <state...>: until the task is in one of the states.
wait_state() {
	local t=$1 s
	shift
	for _ in $(seq 200); do
		s=$(state "$t")
		for want in "$@"; do [[ $s == "$want" ]] && return 0; done
		sleep 0.3
	done
	echo "task $t never reached $* (state $s)" >&2
	return 1
}

# new_task <title> <action-json> [extra json fields]: file a task in
# acme/widgets, script its first run, then approve it (NO_APPROVE=1 leaves
# it proposed) -- filed proposed first so dispatch cannot beat the script.
new_task() {
	local title=$1 action=$2 extra=${3:-}
	local body id
	body=$(jq -nc --arg t "$title" '{title:$t, description:"walkthrough task", repo:"acme/widgets", approve:false}')
	[[ -n $extra ]] && body=$(jq -c ". + $extra" <<<"$body")
	id=$(api POST /api/tasks "$body" | jq -r '.id // .task.id // empty')
	[[ -n $id ]] || { echo "filing $title failed" >&2; return 1; }
	echo "$action" >"$WORK_DIR/action-$id.json"
	if [[ ${NO_APPROVE:-} != 1 ]]; then api POST "/api/tasks/$id/approve" '{}' >/dev/null; fi
	echo "$id"
}

# script_next <task> <action-json>: what the task's next run does.
script_next() { echo "$2" >"$WORK_DIR/action-$1.json"; }
