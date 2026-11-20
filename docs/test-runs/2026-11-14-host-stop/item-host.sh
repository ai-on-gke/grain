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

# item-host.sh <mode>: §7 item 1 on the real systemd --user unit. Two
# detached daemon runs on walk (the stub sleeps 120s and traps SIGTERM),
# then one of:
#
#   stop     systemctl --user stop grain-agentd, then start it again
#   restart  systemctl --user restart grain-agentd
#   kill9    kill -9 grain-agentd's main PID (systemd restarts it)
#   drain    grain-agentd -drain as walkhost (a cutover)
#   usr1     kill -USR1 grain-agentd's main PID (an upgrade drain)
#
# and then prints the stub processes, the unit's cgroup, the daemon's
# log lines for the runs and every attempt's outcome and detail.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
mode=${1:?usage: item-host.sh stop|restart|kill9|drain|usr1}
SLEEP=${SLEEP:-120}
echo "$SLEEP" >"$ROOT/stub-sleep"
A=$("$E" task "$mode run A")
B=$("$E" task "$mode run B")
started "$A" 1
started "$B" 1
echo 2 >"$ROOT/stub-sleep"
echo "runs $A-1 $B-1 in flight on walk"
procs
main=$("$E" hu show -p MainPID --value grain-agentd)
date -u +"%T $mode (grain-agentd pid $main)"
case $mode in
stop)
	"$E" hu stop grain-agentd
	date -u +"%T stop returned"
	sleep 2
	procs
	"$E" hu show -p ActiveState -p SubState grain-agentd
	sleep "${AWAY:-20}"
	runs "$A" "$B"
	"$E" hu start grain-agentd
	date -u +"%T started again"
	;;
restart)
	"$E" hu restart grain-agentd
	date -u +"%T restart returned"
	sleep 2
	procs
	;;
kill9)
	sudo kill -9 "$main"
	sleep 1
	procs
	;;
drain)
	uid=$(id -u walkhost)
	sudo -u walkhost env XDG_RUNTIME_DIR="/run/user/$uid" ~walkhost/.local/bin/grain-agentd -drain
	;;
usr1)
	sudo kill -USR1 "$main"
	;;
esac
"$E" wait-connected || true
sleep "${SETTLE:-25}"
procs
grep -E "run ($A|$B)-[0-9]|hostd walk|walk is back|adopt" "$ROOT/daemon.log" | tail -24 | cut -c1-320
runs "$A" "$B"
