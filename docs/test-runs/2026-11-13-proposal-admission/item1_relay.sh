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

# §5 item 1: one background run proposes (a1) a CI-only cross-repo read,
# (a2) a CI-only repository write, (b) a sandbox shape above the
# maximum and (c) a valid task. Prints the tool results the fake agent
# saw, the proposals grain filed at run end and the parent's conversation.
#
#   STORE=sqlite|postgres ./item1_relay.sh [WORK]

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

WORK=${1:-/tmp/walk-admission-${STORE:-sqlite}}
init_env "$WORK"
if [[ ${STORE:-sqlite} == postgres ]]; then
	# A fresh database for each walk (the role needs CREATEDB).
	psql -q "postgres://grain:grain@127.0.0.1:5432/postgres?sslmode=disable" \
		-c "DROP DATABASE IF EXISTS grain_walk WITH (FORCE)" -c "CREATE DATABASE grain_walk"
fi
start_ghsim "$WORK"
MAX_WORKERS=2 start_daemon "$GRAIN" "$WORK"

echo "== set sandboxShapeLimits (max 2 cpus / 4096 MB / 100 GB)"
api PUT /api/settings '{"sandboxShapeLimits":{"max":{"cpus":2,"memoryMb":4096,"diskGb":100}}}' | jq -c '.sandboxShapeLimits // .'

cat >"$WORK/action-default.json" <<'EOF'
{"kind": "mcp_sequence", "calls": [
  {"tool": "propose_task", "args": {"id": "a1", "title": "A1 CI-only cross-repo read",
    "body": "needs acme/gadgets", "capabilities": ["repo-read:acme/gadgets"]}},
  {"tool": "propose_task", "args": {"id": "a2", "title": "A2 CI-only repository write",
    "body": "wants to push", "capabilities": ["repo-write:acme/widgets"]}},
  {"tool": "propose_task", "args": {"id": "b", "title": "B oversized sandbox",
    "body": "big build", "sandbox_cpus": 16, "sandbox_memory_mb": 65536, "sandbox_disk_gb": 500}},
  {"tool": "propose_task", "args": {"id": "c", "title": "C valid follow-up", "body": "small fix",
    "sandbox_disk_gb": 50}},
  {"tool": "comment_on_issue", "args": {"comment": "proposed four follow-ups"}}
]}
EOF

echo "== file the parent task"
PARENT=$(api POST /api/tasks '{"title":"Parent proposes four","description":"walkthrough parent","repo":"acme/widgets","approved":true}' | jq -r '.id')
echo "parent=$PARENT"

for _ in $(seq 240); do
	state=$(api GET "/api/tasks/$PARENT" | jq -r '.state')
	[[ $state == closed || $state == done || $state == awaiting_reply || $state == failed ]] && break
	sleep 0.5
done
echo "parent state=$state"
sleep 1

echo "== tool results the agent saw"
jq -c '.outputs[] | {tool, isError, text: (.text[0:400])}' "$WORK/runs/mcp-$PARENT.jsonl"

echo "== tasks filed from the run"
api GET "/api/tasks?limit=50" | jq -c '.[] | select(.id != "'"$PARENT"'" and .title != "Director") | {id, title, state}'
for id in $(api GET "/api/tasks?limit=50" | jq -r '.[] | select(.id != "'"$PARENT"'" and .title != "Director") | .id'); do
	echo "-- task $id"
	api GET "/api/tasks/$id" | jq '{id, title, state, reads, capabilities, sandboxCpus, sandboxMemoryMb, sandboxDiskGb, description}'
done

echo "== parent conversation"
api GET "/api/tasks/$PARENT" | jq -r '.comments[]? | "[\(.author)] \(.body[0:600])"'
