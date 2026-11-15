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

# docs/test-plans/2026-11-14.md §5 item 1: two concurrent host-directory
# runs, one holding `stress-ng --cpu 1` and one `--cpu 2` through the
# run's real run_command, sampled through /api/sandboxes every 2 s and
# compared with what pidstat says the same processes used.
#
#   item1_accuracy.sh [seconds]   (default 40)

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
SECS=${1:-40}
OUT="$HERE/out/item1"
mkdir -p "$OUT"

init_env /tmp/walk-cpu
start_ghsim
MAX_WORKERS=2 start_daemon "$GRAIN" "$WORK_DIR"

# file_task <title> <shell>: files a proposed task, gives the fake agent
# its run_command, then approves it. Echoes the task id.
file_task() {
	local title=$1 shell=$2 id
	id=$(api POST /api/tasks "$(jq -nc --arg t "$title" '{title:$t, description:"agents-cpu walkthrough load", repo:"acme/widgets"}')" | jq -r '.id // .task.id')
	jq -nc --arg c "$shell" '{kind:"mcp_sequence", calls:[{tool:"run_command", args:{command:$c, description:"cpu load", timeout:600000}}, {tool:"nothing_needed", args:{reason:"load finished"}}]}' >"$WORK_DIR/action-$id.json"
	api POST "/api/tasks/$id/approve" '{}' >/dev/null
	echo "$id"
}

A=$(file_task "Load one core" "stress-ng --cpu 1 --cpu-method fft --timeout ${SECS}s --quiet")
B=$(file_task "Load two cores" "stress-ng --cpu 2 --cpu-method fft --timeout ${SECS}s --quiet")
echo "tasks: A=$A (1 core) B=$B (2 cores)" | tee "$OUT/tasks.txt"

# Wait until both runs' stress-ng workers exist.
for _ in $(seq 120); do
	n=$(pgrep -c -f 'stress-ng-cpu' || true)
	[[ "$n" -ge 3 ]] && break
	sleep 0.5
done
pgrep -a -f 'stress-ng' >"$OUT/pgrep.txt" || true
# Which run each stress-ng process belongs to, by its working directory.
for pid in $(pgrep -f '^stress-ng'); do echo "$pid $(basename "$(readlink /proc/$pid/cwd)")"; done >"$OUT/pid-run.txt"

# pidstat over the same span, every stress-ng process, 2 s intervals.
pidstat -u -h -C stress-ng 2 $((SECS / 2 - 4)) >"$OUT/pidstat.txt" 2>&1 &
PIDSTAT=$!

: >"$OUT/samples.jsonl"
for i in $(seq $((SECS / 2 - 4))); do
	api GET /api/sandboxes | jq -c --arg t "$(date +%T)" '{t:$t, rows:[.sandboxes[] | {sandbox, backend, name, cpuCores, cpuWindowMS}]}' >>"$OUT/samples.jsonl"
	sleep 2
done
wait "$PIDSTAT" || true

# Per-run totals from pidstat: each stress-ng worker's parent chain
# names the run directory (its sandbox's root).
echo "samples: $(wc -l <"$OUT/samples.jsonl")"
jq -r --arg a "$A" --arg b "$B" '.rows[] | select(.sandbox != null) | "\(.sandbox) \(.cpuCores // "null") \(.cpuWindowMS // "null")"' "$OUT/samples.jsonl" |
	awk '{n[$1]++; if ($2 != "null") {s[$1]+=$2; if (min[$1]==""||$2<min[$1]) min[$1]=$2; if ($2>max[$1]) max[$1]=$2}} END {for (k in n) printf "%s samples=%d mean=%.3f min=%.3f max=%.3f\n", k, n[k], s[k]/n[k], min[k], max[k]}' | sort | tee "$OUT/summary.txt"

# pidstat's per-process %CPU summed per run, averaged over its intervals.
awk 'NR==FNR {run[$1]=$2; next} /^[0-9]/ && ($3 in run) {s[run[$3]]+=$8; t[$1]=1} END {nt=length(t); for (r in s) printf "pidstat %s mean_cores=%.3f intervals=%d\n", r, s[r]/nt/100, nt}' "$OUT/pid-run.txt" "$OUT/pidstat.txt" | sort | tee -a "$OUT/summary.txt"
