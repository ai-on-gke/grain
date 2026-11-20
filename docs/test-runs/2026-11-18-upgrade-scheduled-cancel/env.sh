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

# env.sh is docs/test-plans/2026-11-18.md §4's harness: a real
# `grain daemon` (host sandboxes, the fake agy CLI and GitHub sim of
# docs/test-runs/2026-11-13-grain-health-exit-codes/env.sh) that is also
# an image-mode upgrader over the local registry:2 that
# docs/test-runs/2026-11-13-upgrade-cancel/images.sh fills.
#
#   source env.sh
#   STORE=postgres init_upgrade_env /tmp/w-pg   # or STORE=sqlite
#   start_upgrade_daemon
#   busy_task 1; busy_task 2          # two runs that stay busy
#   release_task 1                    # let run 1 finish
#
# A busy run is a fake-agy run whose one MCP call is a run_command that
# waits for $WALK_DIR/release-<n>; release_task creates that file.
set -euo pipefail
UHERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-13-grain-health-exit-codes/env.sh
source "$UHERE/../2026-11-13-grain-health-exit-codes/env.sh"
REG=${REG:-localhost:5000/acme/grain}

init_upgrade_env() {
	init_env "$1"
	echo "GRAIN_IMAGE=$REG:v1" >"$WORK_DIR/image.env"
	printf '#!/bin/sh\ndate -u +%%T >>%s/restarted\n' "$WORK_DIR" >"$WORK_DIR/restart.sh"
	chmod +x "$WORK_DIR/restart.sh"
}

start_upgrade_daemon() {
	curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null || start_ghsim "$WORK_DIR"
	MAX_WORKERS=${MAX_WORKERS:-4} start_daemon "$WORK_DIR" \
		-upgrade-image "$REG" \
		-upgrade-image-ref-file "$WORK_DIR/image.env" \
		-upgrade-allowed-branches '**' \
		-upgrade-grace-window "${GRACE:-30s}" \
		-upgrade-restart-cmd="$WORK_DIR/restart.sh" "$@"
	export GRAIN_SERVER=$DAEMON GRAIN_DATA_DIR=$WORK_DIR/data
}

# busy_task N [interactive] files and approves a task whose run blocks
# until release_task N. It prints the task id.
busy_task() {
	local n=$1 interactive=${2:-false} id
	id=$(api POST /api/tasks "{\"title\":\"walk busy $n\",\"description\":\"busy run $n\",\"repo\":\"acme/widgets\",\"approve\":false,\"interactive\":$interactive}" | jq -r .id)
	cat >"$WORK_DIR/action-$id.json" <<EOF
{"kind":"mcp_sequence","calls":[{"tool":"run_command","args":{"command":"while [ ! -f $WORK_DIR/release-$n ]; do sleep 1; done; echo released","description":"waiting to be released","timeout":600000}},{"tool":"nothing_needed","args":{"reason":"walk run $n released"}}]}
EOF
	api POST "/api/tasks/$id/approve" '{}' >/dev/null
	echo "$id"
}

release_task() { touch "$WORK_DIR/release-$1"; }

upgrade_json() { api GET /api/upgrade; }
