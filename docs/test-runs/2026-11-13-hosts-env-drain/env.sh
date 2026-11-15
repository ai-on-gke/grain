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

# env.sh: the environment for the 2026-11-13 §11 hosts walkthrough
# (docs/test-runs/2026-11-13-hosts-env-drain.md).
#
#   - a real `grain daemon -sandbox-port-base=0` over SQLite as this user;
#   - a second Linux user, walkhost, with lingering enabled so that its
#     grain-agentd runs as the real systemd --user unit `grain-agentd
#     install` writes (Restart=always), dialling out to the daemon as host
#     `walk`;
#   - stub agy/claude/codex scripts on walkhost's PATH (only the model
#     provider is stubbed): each records argv and environment and sleeps
#     for the seconds in $ROOT/stub-sleep, so drains land mid-run.
#
# Usage: env.sh setup          boot the daemon, enroll and start walk
#        env.sh daemon-start   start the daemon (same data dir)
#        env.sh daemon-stop    stop the daemon (SIGTERM, waits)
#        env.sh task <title>   file and approve a noRepo task on walk
#        env.sh hu <args...>   systemctl --user ... as walkhost
set -euo pipefail

ROOT=${ROOT:-/var/tmp/hed}
BIN=${BIN:-/home/debian/bin}
ADDR=${ADDR:-127.0.0.1:4292}
HOSTUSER=${HOSTUSER:-walkhost}
TOKEN=walkthrough-local-api-token-0123456789abcdef
DAEMON_EXTRA=${DAEMON_EXTRA:-}

api() { curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "$@"; }

hu() {
	local uid
	uid=$(id -u "$HOSTUSER")
	sudo -u "$HOSTUSER" XDG_RUNTIME_DIR="/run/user/$uid" \
		DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" systemctl --user "$@"
}

daemon_start() {
	# shellcheck disable=SC2086
	GCE_METADATA_HOST=127.0.0.1:1 PATH="$ROOT/stubs:$PATH" nohup "$BIN/grain" daemon \
		-data-dir "$ROOT/data" -sandbox-dir "$ROOT/sb" -host-sandboxes \
		-sandbox-port-base=0 -ui-addr "$ADDR" -as walk-owner \
		-poll-interval 1s -max-workers 3 -max-mergers 0 \
		-agy-path "$ROOT/stubs/agy" -gemini-api-key-file "$ROOT/gemini-key" \
		-github-host 127.0.0.1:9 -github-insecure-http $DAEMON_EXTRA \
		>>"$ROOT/daemon.log" 2>&1 </dev/null &
	echo $! >"$ROOT/daemon.pid"
	for _ in $(seq 1 300); do
		api "http://$ADDR/api/hostds" >/dev/null 2>&1 && { echo "daemon up (pid $(cat "$ROOT/daemon.pid"))"; return 0; }
		sleep 0.2
	done
	echo "daemon did not come up" >&2
	tail -20 "$ROOT/daemon.log" >&2
	return 1
}

daemon_stop() {
	[ -f "$ROOT/daemon.pid" ] || return 0
	kill "$(cat "$ROOT/daemon.pid")" 2>/dev/null || true
	while kill -0 "$(cat "$ROOT/daemon.pid")" 2>/dev/null; do sleep 0.1; done
	rm -f "$ROOT/daemon.pid"
	echo "daemon stopped"
}

setup() {
	mkdir -p "$ROOT/data" "$ROOT/sb" "$ROOT/stubs"
	printf '{"directConfigWrites": true}\n' >"$ROOT/data/state-repo.json"
	printf 'walkthrough-dummy-gemini-key\n' >"$ROOT/gemini-key"
	printf '%s\n' "$TOKEN" >"$ROOT/data/local-api-token"
	chmod 600 "$ROOT/data/local-api-token"
	echo 2 >"$ROOT/stub-sleep"
	chmod 644 "$ROOT/stub-sleep"

	id "$HOSTUSER" >/dev/null 2>&1 || sudo useradd -m -s /bin/bash "$HOSTUSER"
	# The sandbox's systemd has no working logind, so `loginctl
	# enable-linger` fails; starting the user manager directly is what
	# lingering does.
	sudo systemctl start "user@$(id -u "$HOSTUSER").service"
	H=$(getent passwd "$HOSTUSER" | cut -d: -f6)
	sudo install -d -o "$HOSTUSER" -g "$HOSTUSER" -m 0755 "$H/.local" "$H/.local/bin"
	for cli in claude codex agy; do
		cat >"$ROOT/stubs/$cli" <<'EOF'
#!/bin/sh
# Stub model-provider CLI: records argv and environment, then sleeps for
# the seconds in /var/tmp/hed/stub-sleep so drains can land mid-run.
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
log=${STUB_LOG_DIR:-$HOME/stub-logs}
[ -n "${GRAIN_STUB_LOG:-}" ] && log=$GRAIN_STUB_LOG
mkdir -p "$log" 2>/dev/null || log=/tmp
{
	echo "argv: $*"
	echo "cwd: $(pwd)"
	env | sort
} >"$log/$name.$$.txt"
s=$(cat /var/tmp/hed/stub-sleep 2>/dev/null || echo 1)
trap 'echo "stub $name got SIGTERM" >>"$log/$name.$$.txt"; exit 143' TERM
sleep "$s" &
wait $!
echo "stub $name slept ${s}s" >>"$log/$name.$$.txt"
echo '{"event":"result","result":{"status":"SUCCESS","response":"stub ok"}}'
EOF
		chmod 755 "$ROOT/stubs/$cli"
		sudo install -o "$HOSTUSER" -g "$HOSTUSER" -m 0755 "$ROOT/stubs/$cli" "$H/.local/bin/$cli"
	done
	sudo install -o "$HOSTUSER" -g "$HOSTUSER" -m 0755 "$BIN/grain-agentd" "$H/.local/bin/grain-agentd"

	daemon_start

	# A legacy per-host token (the sandbox has no identity proxy or device
	# flow for -join), then the real installer as walkhost: it writes the
	# allowlist and the systemd --user unit and starts it.
	tok=$(GRAIN_SERVER="http://$ADDR" GRAIN_TOKEN=$TOKEN "$BIN/grain" host register walk 2>&1 | grep -oE 'ghd_[A-Za-z0-9_-]+' | head -1)
	[ -n "$tok" ] || { echo "no host token"; exit 1; }
	sudo -u "$HOSTUSER" -H env PATH="$H/.local/bin:/usr/bin:/bin" GRAIN_HOSTD_TOKEN="$tok" \
		XDG_RUNTIME_DIR="/run/user/$(id -u "$HOSTUSER")" \
		DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$(id -u "$HOSTUSER")/bus" \
		"$H/.local/bin/grain-agentd" install -server "http://$ADDR" -name walk >"$ROOT/install.out" 2>&1 || { cat "$ROOT/install.out"; exit 1; }
	wait_connected
	api -X PUT "http://$ADDR/api/deployment/agent-backends/walk-agy" \
		-d '{"id":"walk-agy","name":"walk agy","framework":"antigravity","authMode":"ambient","hostd":"walk","models":["gemini-2.5-pro"]}' >/dev/null
}

wait_connected() {
	for _ in $(seq 1 150); do
		s=$(api "http://$ADDR/api/hostds" | jq -r '.[] | select(.name=="walk") | .state')
		[ "$s" = connected ] && { echo "walk connected"; return 0; }
		sleep 0.2
	done
	echo "walk did not connect"
	return 1
}

task() {
	local title=$1
	id=$(api -X POST "http://$ADDR/api/tasks" \
		-d "{\"title\":\"$title\",\"description\":\"walkthrough $title\",\"noRepo\":true,\"agentBackendId\":\"walk-agy\"}" | jq -r .id)
	api -X POST "http://$ADDR/api/tasks/$id/approve" >/dev/null
	echo "$id"
}

case "${1:-}" in
setup) setup ;;
daemon-start) daemon_start ;;
daemon-stop) daemon_stop ;;
wait-connected) wait_connected ;;
task) task "$2" ;;
hu) shift; hu "$@" ;;
*) sed -n '/^# Usage/,/^set -e/p' "$0"; exit 2 ;;
esac
