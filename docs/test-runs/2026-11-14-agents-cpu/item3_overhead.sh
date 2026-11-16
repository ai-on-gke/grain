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

# docs/test-plans/2026-11-14.md §5 items 1 (UI) and 3: ~500 processes on
# the host, two live loaded runs, /next/agents open in three tabs for 60 s.
# strace counts the daemon's /proc sweeps by its readlink of one idle
# sentinel process's cwd, which only sysstat.ProcessTreeCPU reads.

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
OUT="$HERE/out/item3"
mkdir -p "$OUT"
SECS=${SECS:-60}

init_env /tmp/walk-cpu
start_ghsim
MAX_WORKERS=2 start_daemon "$GRAIN" "$WORK_DIR"

file_task() {
	local title=$1 shell=$2 id
	id=$(api POST /api/tasks "$(jq -nc --arg t "$title" '{title:$t, description:"agents-cpu walkthrough load", repo:"acme/widgets"}')" | jq -r '.id // .task.id')
	jq -nc --arg c "$shell" '{kind:"mcp_sequence", calls:[{tool:"run_command", args:{command:$c, description:"cpu load", timeout:600000}}, {tool:"nothing_needed", args:{reason:"load finished"}}]}' >"$WORK_DIR/action-$id.json"
	api POST "/api/tasks/$id/approve" '{}' >/dev/null
	echo "$id"
}
A=$(file_task "Load one core" "stress-ng --cpu 1 --cpu-method fft --timeout 240s --quiet")
B=$(file_task "Load two cores" "stress-ng --cpu 2 --cpu-method fft --timeout 240s --quiet")
echo "tasks: A=$A (1 core) B=$B (2 cores)" | tee "$OUT/tasks.txt"

# Pad the host to about 500 processes with idle sleeps.
mkdir -p "$WORK_DIR/idle"
have=$(ls /proc | grep -c '^[0-9]')
for _ in $(seq $((500 - have))); do (cd "$WORK_DIR/idle" && exec sleep 900) & done
sleep 1
SENTINEL=$!
echo "processes: $(ls /proc | grep -c '^[0-9]') sentinel=$SENTINEL" | tee "$OUT/procs.txt"
for _ in $(seq 120); do
	[[ $(api GET /api/sandboxes | jq '.sandboxes | length') -ge 2 ]] && break
	sleep 0.5
done
sleep 3

DPID=$(cat "$WORK_DIR/daemon.pid")
sudo strace -f -tt -e trace=readlinkat -p "$DPID" -o "$OUT/strace.raw" &
STRACE=$!
sleep 1
node "$HERE/agents_probe.mjs" "$DAEMON" "$CODE" 3 "$SECS" >"$OUT/probe.json"
sudo kill "$STRACE" || true
sleep 1
grep "/proc/$SENTINEL/cwd" "$OUT/strace.raw" | awk '{print $2}' >"$OUT/sweeps.txt" || true
echo "sweeps during the probe: $(wc -l <"$OUT/sweeps.txt")" | tee -a "$OUT/procs.txt"
jq '.tabs' "$OUT/probe.json" | tee -a "$OUT/procs.txt"
# Gaps between sweeps, in seconds: none should be under the 2 s TTL.
awk -F: '{t=$1*3600+$2*60+$3; if (NR>1) printf "%.2f\n", t-p; p=t}' "$OUT/sweeps.txt" | sort -n | head -3 | tr '\n' ' ' | sed 's/^/smallest gaps: /' | tee -a "$OUT/procs.txt"
echo
pkill -f 'sleep 900' || true
