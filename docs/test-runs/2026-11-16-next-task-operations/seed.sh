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

# Seeds about 30 tasks across states on the running §7 environment
# (up.sh first): WORK_DIR=/var/tmp/walk-ops ./seed.sh
# Prints the ids it made as KEY=id lines into $WORK_DIR/seeded.env.
set -uo pipefail
HERE7=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE7/env.sh"
set +e
export WORK_DIR=${WORK_DIR:-/var/tmp/walk-ops}
CODE=$(tr -d '[:space:]' <"$WORK_DIR/data/local-api-token")
export CODE
OUT="$WORK_DIR/seeded.env"
: >"$OUT"
rec() { echo "$1=$2" | tee -a "$OUT"; }
file() { api POST /api/tasks "$1" | jq -r '.id'; }

# 1. A change to .grain/ under the human-required path trigger: the run
# pushes, opens its pull request and finishes; the hold keeps it from
# merging until a person submits.
cat >"$WORK_DIR/action-default.json" <<'JSON'
[{"kind": "mcp_sequence", "calls": [
   {"tool": "run_command", "args": {"command": "cd work && mkdir -p .grain/templates && printf 'name: walk\\ntitle: walk\\nbody: walk\\n' > .grain/templates/walk.yml && git add .grain && git commit -qm 'feat: declare a walkthrough template' -m 'Touches .grain/ so the walk-declarations trigger holds it.' && git push -q origin HEAD && grain pr open", "description": "pushing a .grain change"}},
   {"tool": "comment_on_issue", "args": {"comment": "declared a template"}}]}]
JSON
seed_main_ci
TRIG=$(file '{"title":"Declare a walkthrough template under .grain/","repo":"acme/widgets","approved":true,"autoMerge":true}')
rec TRIG "$TRIG"
wait_state "$TRIG" "awaiting_submit|merging|awaiting_review|completed|closed|failed" 120

# 2. A second finished change outside .grain/ (no hold), for Retry merge.
cat >"$WORK_DIR/action-default.json" <<'JSON'
[{"kind": "mcp_sequence", "calls": [
   {"tool": "run_command", "args": {"command": "cd work && date +%s%N > plain.txt && git add plain.txt && git commit -qm 'chore: touch plain.txt' -m 'Walkthrough commit.' && git push -q origin HEAD && grain pr open", "description": "pushing a plain change"}},
   {"tool": "comment_on_issue", "args": {"comment": "touched plain.txt"}}]}]
JSON
PLAIN=$(file '{"title":"Touch plain.txt","repo":"acme/widgets","approved":true}')
rec PLAIN "$PLAIN"
wait_state "$PLAIN" "awaiting_submit|merging|awaiting_review|completed|closed|failed" 120

# 3. A run that rests on resume_later(ci: my-branch) -- the Fleet "Waiting
# on CI" card.
CIWAIT=$(park_on_ci "Wait on CI for my branch")
rec CIWAIT "$CIWAIT"
echo '[{"kind": "nothing_needed", "reason": "walkthrough default completion"}]' >"$WORK_DIR/action-default.json"

# 4. Pause dispatch; everything filed from here waits for Run now.
api POST /api/pipeline-pause '{"reason":"walkthrough §7: Run now under a pause"}' | jq -c '.pausedNow'
for i in $(seq -w 1 20); do
	rec "Q$i" "$(file "{\"title\":\"Batch run-now row $i\",\"repo\":\"acme/widgets\",\"approved\":true}")"
done
for i in 1 2 3 4; do
	rec "P$i" "$(file "{\"title\":\"Proposed row $i\",\"repo\":\"acme/widgets\"}")"
done
for i in 1 2 3; do
	rec "N$i" "$(file "{\"title\":\"No-repo row $i\",\"noRepo\":true,\"approved\":true}")"
done
for i in 1 2; do
	id=$(file "{\"title\":\"Closed row $i\",\"repo\":\"acme/widgets\"}")
	api POST "/api/tasks/$id/close" '{"reason":"walkthrough: closed for Reopen"}' >/dev/null || api POST "/api/tasks/$id/cancel" '{}' >/dev/null
	rec "C$i" "$id"
done
api GET /api/tasks | jq -r 'group_by(.state) | map("\(.[0].state)=\(length)") | join(" ")'
