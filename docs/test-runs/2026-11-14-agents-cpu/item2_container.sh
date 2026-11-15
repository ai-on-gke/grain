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

# docs/test-plans/2026-11-14.md §5 item 2: a run on the Docker container
# backend (-container-sandboxes), whose /api/sandboxes rows and /next/agents
# card should say "not sampled" with a reason rather than a dash or 0.

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
OUT="$HERE/out/item2"
mkdir -p "$OUT"

init_env /tmp/walk-cpu
start_ghsim
SANDBOX_FLAG=-container-sandboxes start_daemon "$GRAIN" "$WORK_DIR" -container-git-proxy-host 172.17.0.1
id=$(api POST /api/tasks '{"title":"Load one core in a container","description":"agents-cpu walkthrough load","repo":"acme/widgets"}' | jq -r '.id // .task.id')
jq -nc '{kind:"mcp_sequence", calls:[{tool:"run_command", args:{command:"timeout 90 sh -c \"while :; do :; done\"", description:"cpu load", timeout:600000}}, {tool:"nothing_needed", args:{reason:"load finished"}}]}' >"$WORK_DIR/action-$id.json"
api POST "/api/tasks/$id/approve" '{}' >/dev/null
echo "task $id" | tee "$OUT/task.txt"
for _ in $(seq 120); do
	docker ps --format '{{.Names}}' | grep -q . && break
	sleep 1
done
sleep 8
docker ps --format '{{.Names}} {{.Image}} {{.Status}}' | tee "$OUT/docker-ps.txt"
api GET /api/sandboxes | jq '{enabled, sandboxes, sandboxReading}' | tee "$OUT/sandboxes.json"
