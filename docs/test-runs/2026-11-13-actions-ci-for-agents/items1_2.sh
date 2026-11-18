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

# §7 items 1 and 2: grain ci retrigger / --all / run-job from a sandbox on
# a ci_mode: github repository, the refusals, and the host's
# grain ci retrigger gha:... with the grain ci wait line it prints.

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
up

OPEN_PR='cd work && echo "$RANDOM" > walk.txt && git add walk.txt && git commit -qm "feat: walk" -m "Walkthrough." && git push -q origin HEAD && grain pr open 2>&1 | head -3'
A=$(file_task "Task A: retrigger" "{\"kind\":\"mcp_sequence\",\"calls\":[$(SH "$OPEN_PR"),$PARK]}")
B=$(file_task "Task B: someone else" "{\"kind\":\"mcp_sequence\",\"calls\":[$(SH "$OPEN_PR"),$PARK]}")
HA=$(head_of "$A")
HB=$(head_of "$B")
echo "task A=$A branch=$(branch_of "$A") head=$HA pr=$(api GET "/api/tasks/$A" | jq -r .pullRequest)"
echo "task B=$B branch=$(branch_of "$B") head=$HB pr=$(api GET "/api/tasks/$B" | jq -r .pullRequest)"

seed() { # seed <A conclusion> <A attempt> <A status>
	sim POST /_sim/seed-actions "$(jq -nc --arg ha "$HA" --arg hb "$HB" --arg ba "$(branch_of "$A")" --arg bb "$(branch_of "$B")" \
		--arg c "$1" --argjson n "$2" --arg st "$3" '{runs:[
	 {id:9101,name:"Tests",path:".github/workflows/tests.yml",headBranch:$ba,headSha:$ha,headRepo:"acme/widgets",event:"pull_request",status:$st,conclusion:$c,runAttempt:$n,
	  jobs:[{name:"go",conclusion:(if $c=="success" then "success" else "failure" end),failingStep:"go test"},
	        {name:"lint",conclusion:(if $c=="success" then "success" else "failure" end),failingStep:"lint"},
	        {name:"docs",conclusion:"success"}]},
	 {id:9201,name:"Tests",path:".github/workflows/tests.yml",headBranch:$bb,headSha:$hb,headRepo:"acme/widgets",event:"pull_request",status:"completed",conclusion:"failure",
	  jobs:[{name:"go",conclusion:"failure",failingStep:"go test"}]}]}')" >/dev/null
}
seed failure 1 completed
PRHEAD0=$(sim GET /repos/acme/widgets/pulls/9000 | jq -r .head.sha)

echo "== item 1: in-sandbox retrigger, --all, run-job and refusals (task $A)"
calls=$(jq -sc . <<EOF
$(SH 'cd work && echo "local head before: $(git rev-parse HEAD)"')
$(SH 'grain ci retrigger')
$(SH 'grain ci retrigger --all')
$(SH 'timeout 30 grain ci run-job go; echo "run-job exit=$?"')
$(SH 'grain ci retrigger --jobs go,lint')
$(SH 'grain ci retrigger --jobs go,lint,docs')
$(SH 'grain ci retrigger --jobs go,docs')
$(SH 'grain ci retrigger --jobs nosuch')
$(SH "grain ci retrigger --commit $HB")
$(SH 'grain api POST /api/ci/runs/gha:acme:widgets:9201/rerun 2>&1 | head -5')
$(SH 'cd work && echo "local head after: $(git rev-parse HEAD)"')
$PARK
EOF
)
run_once "$A" "{\"kind\":\"mcp_sequence\",\"calls\":$calls}"
last_outputs "$A" | grep -v '^$\|^stderr:$\|^stdout:$'
echo "-- simulator re-run log (run 9101 is A's, 9201 is B's; job ids are run*1000+index: go=0 lint=1 docs=2):"
sim GET /_sim/reruns
echo "PR #9000 head before=$PRHEAD0 after=$(sim GET /repos/acme/widgets/pulls/9000 | jq -r .head.sha) bare=$(head_of "$A")"
echo "PR #9001 (task B) re-runs asked: $(sim GET /_sim/reruns | jq '[.rerunFailedJobs[],.rerunRuns[]]|map(select(.==9201))|length')"

echo "== item 2: host grain ci retrigger gha:acme:widgets:9101"
seed failure 2 completed
out=$("$G" ci retrigger gha:acme:widgets:9101 2>&1) || true
echo "$out"
waitline=$(grep -o 'grain ci wait [^ "]*' <<<"$out" | head -1 || true)
echo "printed wait line: ${waitline:-<none>}"
# The re-run is in progress, then concludes green a few seconds later.
seed "" 3 in_progress
(sleep 4 && seed success 3 completed) &
bg=$!
if [[ -n "$waitline" ]]; then
	start=$(date +%s)
	set +e
	timeout 60 "$G" ${waitline#grain } 2>&1 | tail -n 15
	echo "wait exit=${PIPESTATUS[0]} after $(($(date +%s) - start))s"
	set -e
fi
wait "$bg"
stop_env "$WORK_DIR"
