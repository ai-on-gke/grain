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

# setup.sh: the environment for the 2026-11-11 §9 host agent contract
# walkthrough (docs/test-runs/2026-11-11-host-agent-contract.md).
#
#   - a real `grain daemon -sandbox-port-base=0` over SQLite as this user;
#   - a second Linux user, walkhost, whose PATH carries stub claude, codex
#     and agy shell scripts (only the model providers are stubbed);
#   - grain-agentd built from HEAD in $BIN, and one released agentd
#     (v3.2.0, built from its tag the way scripts/agentd-compat.sh does).
#
# Usage: setup.sh            boot the daemon and prepare walkhost
#        setup.sh restart    restart the daemon only (same data dir)
set -euo pipefail

ROOT=${ROOT:-/var/tmp/hac}
BIN=${BIN:-/home/debian/bin}
ADDR=${ADDR:-127.0.0.1:4291}
HOSTUSER=${HOSTUSER:-walkhost}
TOKEN=walkthrough-local-api-token-0123456789abcdef

start_daemon() {
	GCE_METADATA_HOST=127.0.0.1:1 PATH="$ROOT/stubs:$PATH" nohup "$BIN/grain" daemon \
		-data-dir "$ROOT/data" -sandbox-dir "$ROOT/sb" -host-sandboxes \
		-sandbox-port-base=0 -ui-addr "$ADDR" -as walk-owner \
		-poll-interval 1s -max-workers 1 -max-mergers 0 \
		-agy-path "$ROOT/stubs/agy" -gemini-api-key-file "$ROOT/gemini-key" \
		-github-host 127.0.0.1:9 -github-insecure-http \
		>>"$ROOT/daemon.log" 2>&1 </dev/null &
	echo $! >"$ROOT/daemon.pid"
	for _ in $(seq 1 300); do
		curl -fsS -H "Authorization: Bearer $TOKEN" "http://$ADDR/api/hostds" >/dev/null 2>&1 && return 0
		sleep 0.2
	done
	echo "daemon did not come up" >&2
	tail -20 "$ROOT/daemon.log" >&2
	return 1
}

if [ "${1:-}" = restart ]; then
	kill "$(cat "$ROOT/daemon.pid")" 2>/dev/null || true
	while kill -0 "$(cat "$ROOT/daemon.pid")" 2>/dev/null; do sleep 0.1; done
	start_daemon
	echo "daemon restarted"
	exit 0
fi

mkdir -p "$ROOT/data" "$ROOT/sb" "$ROOT/stubs"
printf '{"directConfigWrites": true}\n' >"$ROOT/data/state-repo.json"
printf 'walkthrough-dummy-gemini-key\n' >"$ROOT/gemini-key"
printf '%s\n' "$TOKEN" >"$ROOT/data/local-api-token"
chmod 600 "$ROOT/data/local-api-token"

# The second Linux user, with the stub CLIs on its PATH.
id "$HOSTUSER" >/dev/null 2>&1 || sudo useradd -m -s /bin/bash "$HOSTUSER"
H=$(getent passwd "$HOSTUSER" | cut -d: -f6)
sudo install -d -o "$HOSTUSER" -g "$HOSTUSER" -m 0755 "$H/.local" "$H/.local/bin"
for cli in claude codex agy; do
	cat >"$ROOT/stubs/$cli" <<'EOF'
#!/bin/sh
# Stub model-provider CLI: records its argv and environment, then behaves
# like a long-ish run so drains and restarts can land mid-run.
name=$(basename "$0")
case "${1:-}" in
--version|-v|version)
	case $name in codex) echo "codex-cli 0.167.0" ;; *) echo "0.0.0 (walkthrough stub $name)" ;; esac
	exit 0 ;;
esac
if [ "$name $*" = "codex debug models" ]; then
	echo '{"models":[{"slug":"gpt-5","display_name":"gpt-5"}]}'
	exit 0
fi
log=${STUB_LOG_DIR:-/home/walkhost/stub-logs}
mkdir -p "$log"
{
	echo "argv: $*"
	echo "cwd: $(pwd)"
	env | sort
	[ -z "${CODEX_HOME:-}" ] || { echo "--- CODEX_HOME"; ls -la "$CODEX_HOME"; }
} >"$log/$name.$$.txt"
echo '{"event":"result","result":{"status":"SUCCESS","response":"stub ok"}}'
sleep "${STUB_SLEEP:-1}"
EOF
	chmod 755 "$ROOT/stubs/$cli"
	sudo install -o "$HOSTUSER" -g "$HOSTUSER" -m 0755 "$ROOT/stubs/$cli" "$H/.local/bin/$cli"
done
sudo install -o "$HOSTUSER" -g "$HOSTUSER" -m 0755 "$BIN/grain-agentd" "$H/.local/bin/grain-agentd"
sudo install -o root -g root -m 0755 "$BIN/grain-agentd-v3.2.0" /usr/local/bin/grain-agentd-v3.2.0
# The daemon user's own grain on walkhost's PATH for `grain hosts`.
start_daemon
echo "daemon up at http://$ADDR (token in $ROOT/data/local-api-token); host user $HOSTUSER home $H"
