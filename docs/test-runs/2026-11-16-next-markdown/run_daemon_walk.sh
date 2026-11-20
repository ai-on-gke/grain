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

# docs/test-plans/2026-11-16.md §6 items 2-3 on a real `grain daemon`
# (-sandbox-port-base=0) with the fake agent CLI and GitHub sim from
# docs/test-runs/2026-11-13-grain-health-exit-codes/. Files three tasks
# whose fake runs (daemon-actions.json) park a long ask_question, a long
# request_access, and leave two GitHub-HTML review findings; raises one
# long alert through POST /api/notifications. Prints the task ids and
# leaves the daemon running for the Playwright pass (inbox-walk.mjs).
#
# STORE=sqlite|postgres ./run_daemon_walk.sh

set -euo pipefail
SELF=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-13-grain-health-exit-codes/env.sh
source "$SELF/../2026-11-13-grain-health-exit-codes/env.sh"

init_env /tmp/walk-md
start_ghsim
start_daemon

wait_state() {
	local id=$1 want=$2
	for _ in $(seq 120); do
		local st
		st=$(api GET "/api/tasks/$id" | jq -r .state)
		[[ $st =~ $want ]] && {
			echo "task $id: $st"
			return 0
		}
		sleep 0.5
	done
	echo "task $id never reached $want (last: $st)" >&2
	return 1
}

file_with() {
	local action=$1 title=$2
	jq ".${action}[0]" "$SELF/daemon-actions.json" >"$WORK_DIR/action-default.json"
	local id
	id=$(api POST /api/tasks "$(jq -n --arg t "$title" '{title: $t, description: "Walkthrough task for §6.", repo: "acme/widgets", approved: true}')" | jq -r .id)
	echo "$id"
}

Q=$(file_with ask "Cache migration rollout question")
wait_state "$Q" 'awaiting_reply|failed|closed' >&2
A=$(file_with access "Cache client access request")
wait_state "$A" 'awaiting|failed|closed' >&2
R=$(file_with review "Review the cache helpers")
wait_state "$R" 'closed|failed|awaiting' >&2
echo '{"kind": "nothing_needed", "reason": "walkthrough default completion"}' >"$WORK_DIR/action-default.json"

body=$(jq -r .reason "$SELF/daemon-actions.json" | sed 's/write access to `acme\/gadgets`/to page someone: the cache tier is degraded/')
api POST /api/notifications "$(jq -n --arg b "$body" '{subject: "cache-tier-degraded", title: "Cache tier degraded", body: $b, severity: "warning"}')" >/dev/null

echo "QUESTION=$Q ACCESS=$A REVIEW=$R DAEMON=$DAEMON CODE=$CODE"
