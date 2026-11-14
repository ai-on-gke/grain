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

# item3-frozen.sh: §11 item 3 with the host's processes still alive.
# Killing grain-agentd under its systemd unit kills the agent CLIs with it
# (KillMode=control-group; item3-away.sh), so to leave live orphans on a
# host that is away while the daemon starts, grain-agentd is frozen with
# SIGSTOP instead: its runs keep running, but it neither answers nor
# reconnects. The daemon is started while it is frozen, and it is thawed
# (SIGCONT) AWAY seconds later. The orphaned processes must be cancelled
# once it is back, not left to run out their 120s.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
E=$HERE/env.sh
ROOT=${ROOT:-/var/tmp/hed}
AWAY=${AWAY:-60}
started() {
	for _ in $(seq 1 120); do
		grep -q "run $1-1's CLI \"agy\" started detached" "$ROOT/daemon.log" && return 0
		sleep 0.5
	done
	return 1
}
echo 120 >"$ROOT/stub-sleep"
A=$("$E" task "frozen run A")
B=$("$E" task "frozen run B")
started "$A"
started "$B"
echo 2 >"$ROOT/stub-sleep"
echo "runs $A-1 $B-1 in flight"
"$E" daemon-stop
"$E" hu kill -s STOP --kill-whom=main grain-agentd
date -u +"grain-agentd frozen (SIGSTOP) at %T"
mark=$(wc -l <"$ROOT/daemon.log")
"$E" daemon-start
date -u +"daemon started at %T (walk away)"
sleep "$AWAY"
echo "--- agent processes before the thaw:"
ps -eo pid,etimes,cmd | grep -E '[a]gy --input' | cut -c1-80 || echo "(none)"
"$E" hu kill -s CONT --kill-whom=main grain-agentd
date -u +"grain-agentd thawed (SIGCONT) at %T"
sleep 15
echo "--- agent processes 15s after the thaw:"
ps -eo pid,etimes,cmd | grep -E '[a]gy --input' | cut -c1-80 || echo "(none)"
tail -n +"$mark" "$ROOT/daemon.log" | grep -E "run ($A|$B)-|hostd walk|abandon|away|cancel" | grep -v 'candidate .* skipped' | head -30 | cut -c1-300
sqlite3 -readonly "$ROOT/data/store/grain.db" \
	"select id, outcome, substr(detail,1,200) from task_run where task_id in ('$A','$B') order by id"
