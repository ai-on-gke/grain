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

# §5 item 3: a run-end proposal naming a template whose `sandbox:` is
# above the deployment maximum (disk 200 against a maximum of 100), and
# one naming a template under it (disk 60). Run after item1_relay.sh on
# the same WORK (it set sandboxShapeLimits' max to 100 GB disk).
#
#   STORE=sqlite|postgres ./item3_template.sh [WORK]

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

WORK=${1:-/tmp/walk-admission-${STORE:-sqlite}}
WORK_DIR=$WORK
CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
export CODE

echo "== templates"
BIG=$(api POST /api/templates '{"name":"big-disk","title":"Big disk work","description":"needs space","repo":"acme/widgets","base":"main","sandbox":{"diskGb":200},"fields":[]}' | jq -r '.id')
SMALL=$(api POST /api/templates '{"name":"small-disk","title":"Small disk work","description":"fits","repo":"acme/widgets","base":"main","sandbox":{"diskGb":60},"fields":[]}' | jq -r '.id')
echo "big=$BIG small=$SMALL"

cat >"$WORK/action-default.json" <<EOF
{"kind": "mcp_sequence", "calls": [
  {"tool": "propose_task", "args": {"id": "t-big", "template_id": "$BIG", "title": "T-big from template", "body": "template fill above max"}},
  {"tool": "propose_task", "args": {"id": "t-small", "template_id": "$SMALL", "title": "T-small from template", "body": "template fill under max"}},
  {"tool": "comment_on_issue", "args": {"comment": "proposed two templated follow-ups"}}
]}
EOF

PARENT=$(api POST /api/tasks '{"title":"Parent proposes from templates","description":"walkthrough parent","repo":"acme/widgets","approved":true}' | jq -r '.id')
echo "parent=$PARENT"
for _ in $(seq 240); do
	[[ -s "$WORK/runs/mcp-$PARENT.jsonl" ]] && break
	sleep 0.5
done
sleep 3
jq -c '.outputs[] | {tool, isError, text: (.text[0:300])}' "$WORK/runs/mcp-$PARENT.jsonl"
for id in $(api GET /api/tasks | jq -r '.[] | select(.title | startswith("T-")) | .id'); do
	api GET "/api/tasks/$id" | jq -c '{id, title, state, sandboxCpus, sandboxMemoryMb, sandboxDiskGb, generatedFrom, description}'
done
