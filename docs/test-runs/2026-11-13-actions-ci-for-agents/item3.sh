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

# §7 item 3: a red tests.yml run on main files exactly one repair, a second
# red run appends to it instead of filing a duplicate, a green run closes
# it, and an unchanged poll of the workflow runs costs only 304s.

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
up
# Every run the repair task gets parks, so it stays open across red runs.
echo '{"kind":"resume_later","mcp_args":{"reason":"repair parked by the walkthrough","when":{"at":"1h"}}}' >"$WORK_DIR/action-default.json"
MAIN=$(git -C "$WORK_DIR/widgets.git" rev-parse main)

runs=()
seed_main() { # seed_main <id> <conclusion> <minutes-ago>
	runs+=("$(jq -nc --argjson id "$1" --arg c "$2" --arg sha "$MAIN" --arg at "$(date -u -d "-$3 min" +%FT%TZ)" \
		'{id:$id,name:"Tests",path:".github/workflows/tests.yml",headBranch:"main",headSha:$sha,headRepo:"acme/widgets",event:"push",
		  status:"completed",conclusion:$c,createdAt:$at,updatedAt:$at,
		  jobs:[{name:"go",conclusion:(if $c=="success" then "success" else "failure" end),failingStep:"go test",
		         log:(if $c=="success" then "ok" else "--- FAIL: TestWidgets (0.01s)\nFAIL" end)}]}')")
	sim POST /_sim/seed-actions "{\"runs\":[$(IFS=,; echo "${runs[*]}")]}" >/dev/null
}
repairs() { api GET '/api/tasks?limit=200' | jq -c '[(if type == "array" then . else .tasks end)[] | select((.title // "") | test("tests|Tests|red|main"; "i")) | select(.title | test("^Task ") | not) | {id, title, state, priority}]'; }
wait_for() { # wait_for <seconds> <jq-filter on repairs()>
	for _ in $(seq $(($1 * 2))); do
		[[ $(repairs | jq "$2") == true ]] && return 0
		sleep 0.5
	done
	return 1
}

echo "== a red tests.yml run on main (run 7001)"
seed_main 7001 failure 10
wait_for 60 'length >= 1' || echo "NO repair filed within 60s"
repairs
R=$(repairs | jq -r '.[0].id // empty')
[[ -n "$R" ]] && api GET "/api/tasks/$R" | jq -r '"repair task \(.id): \(.title) priority=\(.priority) state=\(.state)\n--- body head:\n\(.description // .body | .[0:900])"'

echo "== a second red run (7002) while the repair is open"
seed_main 7002 failure 5
sleep 15
echo "repairs now: $(repairs)"
[[ -n "$R" ]] && echo "comments on $R: $(api GET "/api/tasks/$R" | jq -c '[.comments[]? | {by: (.author // .by), body: (.body | .[0:120])}]')"

echo "== an unchanged poll costs only 304s"
sim DELETE /_sim/requests >/dev/null
sleep 20
sim GET /_sim/requests | jq -c '[.[] | select(.path | test("/actions/runs")) | {p: .path, s: .status}] | group_by([.p, .s]) | map({path: .[0].p, status: .[0].s, n: length})'
echo "non-304 /actions/runs list reads during the idle window: $(sim GET /_sim/requests | jq '[.[] | select(.path | test("/actions/runs($|\\?)")) | select(.status != 304)] | length')"
echo "default-branch reads (GET /repos/acme/widgets): $(sim GET /_sim/requests | jq '[.[] | select(.path == "/repos/acme/widgets")] | length')"

echo "== a green run (7003) closes the loop"
seed_main 7003 success 0
wait_for 60 "map(select(.id == \"$R\" and .state == \"closed\")) | length == 1" || echo "repair $R NOT closed within 60s"
echo "repairs now: $(repairs)"
[[ -n "$R" ]] && api GET "/api/tasks/$R" | jq -c '{state, outcome: (.outcome // .closeOutcome), closing: ((.comments // [])[-1].body // "" | .[0:200])}'
stop_env "$WORK_DIR"
