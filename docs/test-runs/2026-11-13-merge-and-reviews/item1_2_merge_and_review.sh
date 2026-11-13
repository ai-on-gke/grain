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

# Items 1 and 2 of docs/test-plans/2026-11-13.md §6 "What is NOT proved":
# a grain-opened PR touching one unclassified file gets exactly one gating
# review at priority >= 7, the clean review auto-approves, the PR merges,
# and the merge goes out under the connection's merge_credential (ghsim
# records widgets-merger[bot], not the push credential's alice-person).

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
boot /tmp/walk-merge

id=$(file_task "Add a note")
echo "implementation task: $id"
wait_for 90 '[[ $(task '"$id"' | jq -r ".pullRequest // empty") != "" ]]'
pr=$(task "$id" | jq -r .pullRequest)
echo "pull request: $pr"
wait_for 120 '[[ $(task '"$id"' | jq -r ".mergeQueueState // empty") == "merged" || $(task '"$id"' | jq -r .state) == "merged" ]]' || true
echo "--- tasks"
tasks | tee "$OUT/item2-tasks.jsonl"
echo "--- review tasks for $pr"
tasks | jq -c "select(.title == \"Review $pr\")" | tee "$OUT/item2-reviews.jsonl"
echo "--- change's merge gates and state"
task "$id" | jq -c '{state,mergeQueueState,mergeQueueNote,mergeGates}' | tee "$OUT/item2-change.json"
echo "--- ghsim merges"
curl -s "http://$GHSIM/_sim/merges" | tee "$OUT/item1-merges.json"
echo
curl -s "http://$GHSIM/repos/acme/widgets/pulls/${pr##*#}" | jq -c '{merged,merged_by}' | tee "$OUT/item1-pr.json"
echo "--- merged-as event"
sqlite3 "$DB" "select kind, detail from task_event where task_id='$id'" | tee "$OUT/item1-events.txt"
