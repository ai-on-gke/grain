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

# §1 item 3, by runs: drive task $1 through HandoverStaleRuns (10) runs
# that never touch the handover and compare the API, CLI and prompt.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
T=$1
for i in $(seq 10); do
	run_once "$T" "$PARK"
	printf 'run %2d: %s\n' "$i" "$(api GET "/api/tasks/$T/context" | jq -c '{age:.handover_age.runs,comments:.handover_age.comments,stale:.handover_stale}')"
done
"$G" brief "$T" | grep "handover:"
run_once "$T" "$PARK"
f=$(latest_prompt "$T"); [[ $(grep -c 'Before the task below' "$f") -eq 0 ]] || f=$(ls -t "$WORK_DIR"/runs/prompt-"$T"-*.txt | sed -n 2p)
sed 's/\\n/\n/g' "$f" | grep -o "\[grain\] This task's handover is stale[^.]*\." || echo "no stale note in $(basename "$f")"
