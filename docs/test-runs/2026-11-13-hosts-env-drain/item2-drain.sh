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

# item2-drain.sh: §11 item 2, a plain drain under load with a daemon
# restart in the middle. Needs env.sh setup first.
#
#   run A: daemon task, detached on walk, stub sleeps 8s   (finishes in the drain)
#   run B: daemon task, detached on walk, stub sleeps 120s (killed at drain_cap)
#   run C: drainprobe's own start, attached over the socket, sleeps 10s
#
# drain_cap is set to 25s. The daemon is stopped 2s into the drain and
# started again 15s in, after A has ended, so A's Exit is one the daemon
# never read; B ends at the cap with Exit.Draining while the new daemon
# is attached. The host then exits 0 and systemd (Restart=always) starts
# it again.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
E=$HERE/env.sh
ROOT=${ROOT:-/var/tmp/hed}
ALLOW=/home/walkhost/.config/grain/agentd.yaml
SOCK=/run/user/$(id -u walkhost)/grain-agentd/agentd.sock

sudo -u walkhost sh -c "grep -q '^drain_cap:' $ALLOW || printf 'drain_cap: 25s\n' >>$ALLOW"

started() { # wait for "run <id>-1's CLI ... started detached"
	for _ in $(seq 1 120); do
		grep -q "run $1-1's CLI \"agy\" started detached" "$ROOT/daemon.log" && return 0
		sleep 0.5
	done
	echo "run $1-1 did not start" >&2
	return 1
}

echo 8 >"$ROOT/stub-sleep"
A=$("$E" task "drain run A (8s)")
started "$A"
echo 120 >"$ROOT/stub-sleep"
B=$("$E" task "drain run B (120s)")
started "$B"
echo 10 >"$ROOT/stub-sleep"
echo "A=$A B=$B" | tee "$ROOT/item2-ids"
date -u +"drain at %T" | tee -a "$ROOT/item2-ids"
sudo -u walkhost /var/tmp/drainprobe -socket "$SOCK" >"$ROOT/item2-drainprobe.txt" 2>&1 &
probe=$!
sleep 3
"$E" daemon-stop
date -u +"daemon stopped at %T" | tee -a "$ROOT/item2-ids"
sleep 13
"$E" daemon-start
date -u +"daemon started at %T" | tee -a "$ROOT/item2-ids"
wait "$probe" || true
cat "$ROOT/item2-drainprobe.txt"
echo 2 >"$ROOT/stub-sleep"
