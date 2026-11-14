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

# item3-away.sh: §11 item 3, a host away while the daemon starts. Two
# daemon runs in flight on walk (stub sleeps 90s); stop the daemon; kill
# grain-agentd's main process with SIGKILL (systemd starts it again
# after RestartSec=10s); start the daemon at once, while walk is away.
# The runs must be finished with a clear reason, and their processes
# cancelled once walk is back, not left running for 90s.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
E=$HERE/env.sh
ROOT=${ROOT:-/var/tmp/hed}
started() {
	for _ in $(seq 1 120); do
		grep -q "run $1-1's CLI \"agy\" started detached" "$ROOT/daemon.log" && return 0
		sleep 0.5
	done
	return 1
}
echo 90 >"$ROOT/stub-sleep"
A=$("$E" task "away run A")
B=$("$E" task "away run B")
started "$A"
started "$B"
echo 2 >"$ROOT/stub-sleep"
echo "runs $A-1 $B-1 in flight"
ps -eo pid,pgid,etimes,cmd | grep -E '[a]gy --input' | cut -c1-80
"$E" daemon-stop
"$E" hu kill -s KILL --kill-whom=main grain-agentd
date -u +"grain-agentd SIGKILLed at %T"
sleep 1
echo "--- agent processes right after the kill:"
ps -eo pid,pgid,etimes,cmd | grep -E '[a]gy --input' | cut -c1-80 || echo "(none)"
"$E" daemon-start
date -u +"daemon started at %T (walk away)"
sleep 25
echo "--- agent processes 25s later (walk back):"
ps -eo pid,pgid,etimes,cmd | grep -E '[a]gy --input' | cut -c1-80 || echo "(none)"
grep -E "run ($A|$B)-1|hostd walk|abandon|away" "$ROOT/daemon.log" | tail -20 | cut -c1-300
sqlite3 -readonly "$ROOT/data/store/grain.db" \
	"select id, outcome, substr(detail,1,200) from task_run where task_id in ('$A','$B') order by id"
