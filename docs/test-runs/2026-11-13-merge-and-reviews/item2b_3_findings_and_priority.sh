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

# Item 2 (findings half) and item 3 of docs/test-plans/2026-11-13.md §6.
#   2b. A review that leaves a finding (add_review_comment) holds the
#       change: auto-merge stays off and the PR is not merged.
#   3.  With one worker slot busy and three priority-5 tasks queued behind
#       it, the gating review of a priority-9 change dispatches first.

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
MAX_WORKERS=1 boot /tmp/walk-merge

echo "=== 2b. a review with findings"
cat >"$WORK_DIR/action-review.json" <<'J'
{"kind":"mcp_sequence","calls":[
 {"tool":"add_review_comment","args":{"body":"This note has no trailing context; say why it exists.","path":"README.md","line":1}},
 {"tool":"comment_on_issue","args":{"comment":"One finding."}}
]}
J
id=$(file_task "Add a note with a finding")
wait_for 120 '[[ $(tasks | jq -r "select(.title|startswith(\"Review \")) | .state" | head -1) == closed ]]'
sleep 20
tasks | tee "$OUT/item2b-tasks.jsonl"
task "$id" | jq -c '{state,autoMerge,mergeQueueState,mergeQueueNote,mergeGates,questions: ([.questions[]?|.text[0:200]])}' | tee "$OUT/item2b-change.json"
curl -s "http://$GHSIM/repos/acme/widgets/pulls/9000" | jq -c '{state,merged}' | tee "$OUT/item2b-pr.json"
sqlite3 "$DB" "select kind, detail from task_event where task_id='$id'" | tee "$OUT/item2b-events.txt"
clean_review_action >"$WORK_DIR/action-review.json"

echo "=== 3. priority-9 change's review vs a full queue"
next=$(($(api GET '/api/tasks?limit=100' | jq -r '[.[].id|tonumber]|max') + 1))
push_action | jq '. + {hold: true, hold_seconds: 120}' >"$WORK_DIR/action-$next.json"
hi=$(file_task "Urgent change" 9)
[[ $hi == "$next" ]] || { echo "expected task $next, got $hi" >&2; exit 1; }
wait_for 60 "[[ -f $WORK_DIR/live-$hi.hold ]]"
low=()
for n in 1 2 3; do
	echo '{"kind":"nothing_needed","reason":"low priority filler","hold":true,"hold_seconds":3}' >"$WORK_DIR/action-$((hi + n)).json"
	low+=("$(file_task "Filler $n" 5)")
done
echo "slot busy with $hi; queued: ${low[*]}"
rm -f "$WORK_DIR/live-$hi.hold"
wait_for 180 '[[ $(tasks | jq -r "select(.title|startswith(\"Review \")) | .id" | wc -l) -ge 2 ]]'
wait_for 180 '[[ $(for t in '"${low[*]}"'; do task $t | jq -r .state; done | grep -c -v queued) -eq 3 ]]'
sleep 5
echo "--- dispatch order (first run start per task)"
sqlite3 "$DB" "select task_id, min(started_at) from task_run group by task_id order by min(started_at)" | tee "$OUT/item3-dispatch-order.txt"
tasks | tee "$OUT/item3-tasks.jsonl"
