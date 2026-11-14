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

# Item 4 of docs/test-plans/2026-11-13.md §6: an approved review is
# carried across a merge of main that leaves the PR's diff unchanged, and
# a head move that changes the diff files an incremental re-review.
# The fake agent seeds an in-progress "unit" check on its branch before it
# pushes, so the change waits on CI (and does not merge) while the head is
# moved by hand.

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
boot /tmp/walk-merge
cat >"$WORK_DIR/action-default.json" <<J
{"kind":"mcp_sequence","calls":[
 {"tool":"run_command","args":{"command":"cd work && br=\$(git rev-parse --abbrev-ref HEAD) && curl -s -X POST http://$GHSIM/_sim/check-runs -d '{\"ref\":\"'\$br'\",\"checks\":[{\"Name\":\"unit\",\"Status\":\"in_progress\"}]}' && mkdir -p notes && echo 'change from task {TASK}' > notes/{TASK}.txt && git add -A && git commit -qm 'docs: note from task {TASK}' -m 'Walkthrough change.' && git push -q origin HEAD","description":"pushing a one-file change behind a pending check"}},
 {"tool":"comment_on_issue","args":{"comment":"pushed"}}
]}
J
reviews() { tasks | jq -c 'select(.title|startswith("Review "))'; }
gate() { task "$1" | jq -c '{state,mergeQueueState,mergeQueueNote,review: (.mergeGates[]?|select(.id=="review"))}'; }

id=$(file_task "Add a note")
wait_for 120 '[[ $(reviews | jq -r .state) == closed ]]'
wait_for 60 '[[ $(task '"$id"' | jq -r "[.mergeGates[]?|select(.id==\"review\")|.state][0]") == passed ]]'
echo "--- approved"; reviews; gate "$id" | tee "$OUT/item4-approved.json"
branch=$(task "$id" | jq -r .branch)
head0=$(git -C "$WORK_DIR/widgets.git" rev-parse "$branch")

echo "=== 4a. merge main into the PR without changing its diff"
clone=$(mktemp -d)
git clone -q "$WORK_DIR/widgets.git" "$clone"
g() { git -C "$clone" -c user.name="Walk Person" -c user.email=person@example.com "$@"; }
echo unrelated >"$clone/UNRELATED.md"
g add UNRELATED.md && g commit -qm "docs: unrelated change on main" && g push -q origin main
g checkout -q "$branch" && g merge -q --no-edit origin/main && g push -q origin "$branch"
echo "head $head0 -> $(g rev-parse HEAD)"
sleep 45
reviews | tee "$OUT/item4a-reviews.jsonl"
gate "$id" | tee "$OUT/item4a-gate.json"

echo "=== 4b. a push that changes the diff"
echo "second line" >>"$clone/notes/$id.txt"
g commit -qam "docs: extend the note" && g push -q origin "$branch"
wait_for 120 '[[ $(reviews | wc -l) -ge 2 ]]' || true
sleep 10
reviews | tee "$OUT/item4b-reviews.jsonl"
gate "$id" | tee "$OUT/item4b-gate.json"
last=$(reviews | tail -1 | jq -r .id)
api GET "/api/tasks/$last" | jq -r .description | head -40 >"$OUT/item4b-rereview-body.txt"
grep -n -i 'since\|increment\|new commits\|only' "$OUT/item4b-rereview-body.txt" | head -8
