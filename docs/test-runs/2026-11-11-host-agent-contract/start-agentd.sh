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

# start-agentd.sh: start walkhost's grain-agentd the way its installed
# grain-agentd.service would (the sandbox has no systemd --user session for
# a second user). BIN picks the binary: the HEAD build installed in
# ~/.local/bin by default, or /usr/local/bin/grain-agentd-v3.2.0.
#
# Usage: start-agentd.sh [start|stop]
set -euo pipefail
ROOT=${ROOT:-/var/tmp/hac}
HOSTUSER=${HOSTUSER:-walkhost}
H=$(getent passwd "$HOSTUSER" | cut -d: -f6)
BIN=${AGENTD_BIN:-$H/.local/bin/grain-agentd}
ALLOW=${ALLOW:-$H/.config/grain/agentd.yaml}
pidf=$ROOT/agentd.pid

if [ -f "$pidf" ]; then
	sudo kill "$(cat "$pidf")" 2>/dev/null || true
	while sudo kill -0 "$(cat "$pidf")" 2>/dev/null; do sleep 0.1; done
	rm -f "$pidf"
fi
[ "${1:-start}" = stop ] && exit 0

sudo install -d -o "$HOSTUSER" -g "$HOSTUSER" -m 0700 "/run/user-$HOSTUSER"
sudo -u "$HOSTUSER" -H env PATH="$H/.local/bin:/usr/bin:/bin" XDG_RUNTIME_DIR="/run/user-$HOSTUSER" \
	STUB_SLEEP="${STUB_SLEEP:-1}" \
	sh -c 'echo $$ >"$0"; exec "$@"' "/run/user-$HOSTUSER/agentd.pid" \
	"$BIN" -server "http://${ADDR:-127.0.0.1:4291}" \
	-token-file "$H/.config/grain/agentd-token" -allowlist "$ALLOW" \
	-state-dir "$H/.local/state/grain-agentd" -name walk \
	>>"$ROOT/agentd.log" 2>&1 </dev/null &
for _ in $(seq 1 50); do [ -s "/run/user-$HOSTUSER/agentd.pid" ] && break; sleep 0.1; done
sudo cat "/run/user-$HOSTUSER/agentd.pid" >"$pidf"
T=$(cat "$ROOT/data/local-api-token")
for _ in $(seq 1 100); do
	c=$(curl -fsS -H "Authorization: Bearer $T" "http://${ADDR:-127.0.0.1:4291}/api/hostds" |
		jq -r '.[] | select(.name=="walk") | .state')
	[ "$c" = connected ] && { echo "walk connected ($BIN)"; exit 0; }
	sleep 0.2
done
echo "walk did not connect"; tail -20 "$ROOT/agentd.log"; exit 1
