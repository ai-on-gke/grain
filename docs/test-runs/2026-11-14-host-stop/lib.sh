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

# lib.sh: helpers the item scripts source (docs/test-runs/2026-11-14-host-stop.md).
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
E=$HERE/env.sh
ROOT=${ROOT:-/var/tmp/hs}
ADDR=${ADDR:-127.0.0.1:4293}
TOKEN=walkthrough-local-api-token-0123456789abcdef
api() { curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "$@"; }

# started <task> <attempt>: wait for the run's CLI to start detached on walk.
started() {
	for _ in $(seq 1 240); do
		grep -q "run $1-$2's CLI \"agy\" started detached" "$ROOT/daemon.log" && return 0
		sleep 0.5
	done
	echo "run $1-$2 never started" >&2
	return 1
}

# procs: every stub agent CLI process and the cgroup that holds them.
procs() {
	echo "--- pgrep -u walkhost -af 'agy|sleep':"
	pgrep -u walkhost -af '\.local/bin/agy|^sleep' || echo "(none)"
	echo "--- systemd-cgls grain-agentd.service:"
	sudo systemd-cgls --no-pager -u "user@$(id -u walkhost).service" 2>/dev/null |
		sed -n '/grain-agentd.service/,/^[│ ]*[├└]─[a-z].*\.service$/p' | head -12
}

# runs <task...>: every attempt of the tasks, with its outcome and detail.
runs() {
	for t in "$@"; do
		api "http://$ADDR/api/tasks/$t" | jq -r '"task \(.id) state=\(.state)",
			(.attempts[]? | "  run \(.number) outcome=\(.outcome // "running") detail=\(.detail // "" | .[0:300])")'
	done
}
