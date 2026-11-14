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

# §7 item 4: an auto-merge task's pull request whose every failing Actions
# job died at setup on "Temporary failure resolving" is waited out with a
# re-run of the failed jobs, not sent back for a repair; once the outage
# clears it merges. A control task failing on a real test is repaired.

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
up -max-mergers 1

OPEN_PR='cd work && echo "$RANDOM" > walk.txt && git add walk.txt && git commit -qm "feat: walk" -m "Walkthrough." && git push -q origin HEAD && grain pr open 2>&1 | head -2'
FINISH='{"tool":"comment_on_issue","args":{"comment":"done"}}'
file_auto() { # file_auto <title>: an auto-merge task whose one run opens its PR and finishes
	local id
	id=$(api POST /api/tasks "{\"title\":\"$1\",\"body\":\"walkthrough\",\"repo\":\"acme/widgets\",\"autoMerge\":true}" | jq -r .id)
	echo "{\"kind\":\"mcp_sequence\",\"calls\":[$(SH "$OPEN_PR"),$FINISH]}" >"$WORK_DIR/action-$id.json"
	echo '{"kind":"resume_later","mcp_args":{"reason":"repair run parked by the walkthrough","when":{"at":"1h"}}}' >"$WORK_DIR/action-default.json"
	api POST "/api/tasks/$id/approve" '{}' >/dev/null
	sleep 2
	wait_rest "$id" >&2
	echo "$id"
}
D=$(file_auto "Task D: DNS outage")
C=$(file_auto "Task C: real failure")
HD=$(head_of "$D")
HC=$(head_of "$C")
echo "task D=$D pr=$(api GET "/api/tasks/$D" | jq -r .pullRequest) head=$HD"
echo "task C=$C pr=$(api GET "/api/tasks/$C" | jq -r .pullRequest) head=$HC"

DNS_LOG=$'##[group]Run sudo apt-get update\nIgn:1 http://archive.ubuntu.com/ubuntu noble InRelease\nErr:1 http://archive.ubuntu.com/ubuntu noble InRelease\n  Temporary failure resolving \'archive.ubuntu.com\'\nE: Unable to fetch some archives\n##[endgroup]\n##[error]Process completed with exit code 100.\n'
TEST_LOG=$'##[group]Run go test ./...\n--- FAIL: TestWidgets (0.01s)\n    widgets_test.go:12: got 2, want 3\nFAIL\n##[endgroup]\n##[error]Process completed with exit code 1.\n'
AGO=$(date -u -d '-10 min' +%FT%TZ)
seed_runs() { # seed_runs <D conclusion> <D attempt> [D status]
	sim POST /_sim/seed-actions "$(jq -nc --arg hd "$HD" --arg hc "$HC" --arg bd "$(branch_of "$D")" --arg bc "$(branch_of "$C")" \
		--arg dns "$DNS_LOG" --arg test "$TEST_LOG" --arg at "$AGO" --arg dc "$1" --argjson dn "$2" --arg ds "${3:-completed}" '{runs:[
	 {id:8101,name:"Tests",path:".github/workflows/tests.yml",headBranch:$bd,headSha:$hd,headRepo:"acme/widgets",event:"pull_request",status:$ds,conclusion:$dc,runAttempt:$dn,createdAt:$at,updatedAt:$at,
	  jobs:[{name:"go",conclusion:(if $dc=="success" then "success" else "failure" end),failingStep:"Install system dependencies",log:(if $dc=="success" then "ok" else $dns end)},
	        {name:"ui e2e",conclusion:(if $dc=="success" then "success" else "failure" end),failingStep:"Install system dependencies",log:(if $dc=="success" then "ok" else $dns end)}]},
	 {id:8201,name:"Tests",path:".github/workflows/tests.yml",headBranch:$bc,headSha:$hc,headRepo:"acme/widgets",event:"pull_request",status:"completed",conclusion:"failure",runAttempt:1,createdAt:$at,updatedAt:$at,
	  jobs:[{name:"go",conclusion:"failure",failingStep:"go test",log:$test}]}]}')" >/dev/null
}
checks() { # checks <sha> <conclusion> [status]
	sim POST /_sim/check-runs "$(jq -nc --arg r "$1" --arg c "$2" --arg s "${3:-completed}" '{ref:$r,checks:[{id:1,name:"Tests / go",status:$s,conclusion:$c},{id:2,name:"Tests / ui e2e",status:$s,conclusion:$c}]}')" >/dev/null
}
show() { api GET "/api/tasks/$1" | jq -c '{id, state, attempts: (.attempts | length), mergeQueueState, mergeQueueNote, repairs: (.repairAttempts // .repairCount // null)}'; }

echo "== both PRs go red: D on the runner's DNS, C on a test"
seed_runs failure 1
checks "$HD" failure
checks "$HC" failure
for _ in $(seq 60); do
	[[ $(sim GET /_sim/reruns | jq '.rerunFailedJobs | index(8101) != null') == true ]] && break
	sleep 0.2
done
# As on GitHub, the re-run starts attempt 2 and the checks go pending.
seed_runs "" 2 in_progress
checks "$HD" "" in_progress
sleep 8
echo "simulator re-runs: $(sim GET /_sim/reruns)"
show "$D"
show "$C"
echo "D comments: $(api GET "/api/tasks/$D" | jq -c '[.comments[]? | .body[0:160]]')"
echo "C comments: $(api GET "/api/tasks/$C" | jq -c '[.comments[]? | .body[0:160]]')"
grep -h 'failed on the runner\|no repair counted\|requeue\|repair' "$WORK_DIR/daemon.log" | grep -v 'github-base-repair' | tail -n 8 | cut -c1-400

echo "== the outage clears: D's re-run (attempt 2) goes green"
seed_runs success 2
checks "$HD" success
sim POST "/_sim/mergeable?n=9000" >/dev/null
for _ in $(seq 120); do
	[[ $(api GET "/api/tasks/$D" | jq -r .state) == closed ]] && break
	sleep 1
done
show "$D"
sim GET /repos/acme/widgets/pulls/9000 | jq -c '{state, merged, mergeable, mergeable_state, head: .head.sha[0:12]}'
echo "PR #$(api GET "/api/tasks/$D" | jq -r '.pullRequest | split("#")[1]') merged=$(sim GET "/repos/acme/widgets/pulls/$(api GET "/api/tasks/$D" | jq -r '.pullRequest | split("#")[1]')" | jq -r '.merged')"
stop_env "$WORK_DIR"
