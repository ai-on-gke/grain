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

# item2-handover.sh: §11 item 2 with the real CLI. Three daemon runs in
# flight on walk (stub sleeps 20s), then `grain-agentd -drain` as
# walkhost (the flag F1 added): a host speaking this build's protocol cuts
# over -- it keeps taking starts and exits the moment nothing runs -- so
# the three runs must finish, their Exit frames reach the daemon, and
# systemd (Restart=always) must start grain-agentd again.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
E=$HERE/env.sh
ROOT=${ROOT:-/var/tmp/hed}
SOCK=/run/user/$(id -u walkhost)/grain-agentd/agentd.sock
started() {
	for _ in $(seq 1 120); do
		grep -q "run $1-1's CLI \"agy\" started detached" "$ROOT/daemon.log" && return 0
		sleep 0.5
	done
	return 1
}
echo 20 >"$ROOT/stub-sleep"
ids=()
for n in 1 2 3; do
	id=$("$E" task "handover run $n")
	ids+=("$id")
done
for id in "${ids[@]}"; do started "$id"; done
echo 2 >"$ROOT/stub-sleep"
pid0=$("$E" hu show -p MainPID --value grain-agentd)
echo "runs ${ids[*]} in flight; grain-agentd pid $pid0"
date -u +"grain-agentd -drain at %T"
sudo -u walkhost XDG_RUNTIME_DIR=/run/user/$(id -u walkhost) /home/walkhost/.local/bin/grain-agentd -drain && echo "grain-agentd -drain: exit 0"
sleep 40
pid1=$("$E" hu show -p MainPID --value grain-agentd)
echo "grain-agentd pid after: $pid1 (a new pid: systemd restarted it)"
for id in "${ids[@]}"; do
	grep -E "run $id-1" "$ROOT/daemon.log" | tail -2
done
