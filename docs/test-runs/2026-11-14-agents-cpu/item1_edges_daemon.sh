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

# docs/test-plans/2026-11-14.md §5 item 1 edge cases through a real
# daemon's run_command (which a deployment with a data directory confines
# under bwrap): a child that cd's to /tmp, a double fork that cd's to /tmp,
# and a burst of short reaped children whose parent exits half way.
#
#   item1_edges_daemon.sh [seconds]   (default 24)

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
SECS=${1:-24}
OUT="$HERE/out/item1-edges"
mkdir -p "$OUT"

init_env /tmp/walk-cpu
start_ghsim
MAX_WORKERS=3 start_daemon "$GRAIN" "$WORK_DIR"

file_task() {
	local title=$1 shell=$2 id
	id=$(api POST /api/tasks "$(jq -nc --arg t "$title" '{title:$t, description:"agents-cpu walkthrough load", repo:"acme/widgets"}')" | jq -r '.id // .task.id')
	jq -nc --arg c "$shell" '{kind:"mcp_sequence", calls:[{tool:"run_command", args:{command:$c, description:"cpu load", timeout:600000}}, {tool:"nothing_needed", args:{reason:"load finished"}}]}' >"$WORK_DIR/action-$id.json"
	api POST "/api/tasks/$id/approve" '{}' >/dev/null
	echo "$id"
}

T=$(file_task "Load one core from /tmp" "cd /tmp && stress-ng --cpu 1 --cpu-method fft --timeout ${SECS}s --quiet")
F=$(file_task "Double fork to /tmp then wait" "(cd /tmp && setsid stress-ng --cpu 1 --cpu-method fft --timeout ${SECS}s --quiet </dev/null >/dev/null 2>&1 &); sleep ${SECS}")
H=$((SECS / 2))
B=$(file_task "Burst of short children" "end=\$((SECONDS+$H)); while [ \$SECONDS -lt \$end ]; do sh -c 'i=0; while [ \$i -lt 3000 ]; do i=\$((i+1)); done'; done; sleep $H")
echo "tasks: cd-tmp=$T double-fork=$F burst=$B" | tee "$OUT/tasks.txt"

for _ in $(seq 120); do
	[[ $(api GET /api/sandboxes | jq '.sandboxes | length') -ge 3 ]] && break
	sleep 0.5
done
sleep 2
# Where the double-forked stress-ng went: its parent and working directory.
for pid in $(pgrep -f '^stress-ng'); do
	echo "$pid ppid=$(awk '{print $4}' /proc/$pid/stat) ppid_comm=$(cat /proc/$(awk '{print $4}' /proc/$pid/stat)/comm) cwd=$(readlink /proc/$pid/cwd)"
done >"$OUT/stress-ng-procs.txt"
: >"$OUT/samples.jsonl"
for _ in $(seq $((SECS / 2 - 2))); do
	api GET /api/sandboxes | jq -c --arg t "$(date +%T)" '{t:$t, rows:[.sandboxes[] | {sandbox, cpuCores, cpuWindowMS}]}' >>"$OUT/samples.jsonl"
	sleep 2
done
jq -r '.t + " " + ([.rows[] | "\(.sandbox)=\(.cpuCores // "absent" | if type == "number" then (. * 1000 | round / 1000) else . end)"] | join(" "))' "$OUT/samples.jsonl" | tee "$OUT/summary.txt"
