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

# Real-daemon environment for docs/test-plans/2026-11-15.md §2: the
# 2026-11-13 health harness (real `grain daemon -sandbox-port-base=0`, fake
# agy CLI calling the sandbox MCP, GitHub sim) with this directory's ghsim.go,
# which adds review-thread and conversation-resolution control routes.

RL_HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-13-grain-health-exit-codes/env.sh
source "$RL_HERE/../2026-11-13-grain-health-exit-codes/env.sh"

init_rl_env() {
	init_env "$1"
	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$RL_HERE/ghsim.go")
}

# seed_main_ci gives acme/widgets one finished Actions run on main, so a
# resume_later(ci: my-branch) is accepted (pkg/ui/wait_task.go refuses a CI
# wait on a repository with no CI at all) while the task's own branch never
# gets a verdict until the walkthrough seeds one.
seed_main_ci() {
	curl -fsS -X POST "http://$GHSIM/_sim/seed-actions" -H "Content-Type: application/json" \
		-d '{"runs":[{"id":5001,"name":"CI","path":".github/workflows/ci.yml","headBranch":"main","headSha":"0000000000000000000000000000000000000001","event":"push","status":"completed","conclusion":"success"}]}' >/dev/null
}

# state prints task $1's state.
state_of() { api GET "/api/tasks/$1" | jq -r '.state'; }

# wait_state waits up to $3 seconds (default 30) for task $1 to reach any
# of the |-separated states in $2, and prints the state it ended in.
wait_state() {
	local id=$1 want=$2 secs=${3:-30} st=""
	for _ in $(seq $((secs * 4))); do
		st=$(state_of "$id")
		if [[ "|$want|" == *"|$st|"* ]]; then
			echo "$st"
			return 0
		fi
		sleep 0.25
	done
	echo "$st"
	return 1
}

# runs_of prints how many runs task $1 has had.
runs_of() { api GET "/api/tasks/$1" | jq -r '(.runs // []) | length'; }

# comments_of prints task $1's conversation, one body per paragraph.
comments_of() { api GET "/api/tasks/$1" | jq -r '(.comments // [])[] | "[\(.author.kind // .authorKind // "?")] \(.body)"'; }

# park_on_ci files a task whose run pushes its branch, opens its pull
# request with `grain pr open` (as a real agent is told to) and calls
# resume_later(ci: my-branch). It waits for the task to rest deferred with
# its pull request open in the sim and prints its id.
park_on_ci() {
	local title=$1 extra=${2:-}
	cat >"$WORK_DIR/action-default.json" <<'EOF'
[{"kind": "mcp_sequence", "calls": [
   {"tool": "run_command", "args": {"command": "cd work && date +%s%N > walk.txt && git add walk.txt && git commit -qm 'chore: touch walk.txt' -m 'Walkthrough commit.' && git push -q origin HEAD && grain pr open", "description": "pushing the branch and opening its pull request"}},
   {"tool": "resume_later", "args": {"reason": "waiting for CI on my branch", "when": {"ci": "my-branch"}}}]}]
EOF
	local id
	seed_main_ci
	id=$(api POST "/api/tasks" "{\"title\":\"$title\",\"repo\":\"acme/widgets\",\"approved\":true$extra}" | jq -r '.id')
	if ! wait_state "$id" deferred 90 >/dev/null; then
		echo "task $id never rested deferred (state $(state_of "$id"))" >&2
		return 1
	fi
	echo "$id"
}
