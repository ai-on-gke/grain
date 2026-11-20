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

# docs/test-plans/2026-11-14.md §3 item 1: /api/config ETag/304 across a
# real usage-limit pause on a real `grain daemon -sandbox-port-base=0`.
#
#   STORE=sqlite   ./pause.sh
#   STORE=postgres PG_URL=postgres://... ./pause.sh
#
# Usage-limit pauses are what a run's UsageLimitError begins; the daemon
# persists each in `usage_limit_pause` and restores it at startup
# (orchestrator.Pause.Restore). The script writes the rows the store
# would hold, starts the daemon with TZ differing from the team zone,
# and walks: steady 304s while secondsRemaining ticks, a second pause,
# an extension (each across a restart, the path a running daemon's own
# Begin persists through), and ending one early with DELETE /api/pause
# while the daemon runs.

set -euo pipefail
GRAIN=${GRAIN:-/home/debian/bin/grain}
STORE=${STORE:-sqlite}
PORT=${PORT:-4282}
WORK=${WORK:-/tmp/heavy-pause-$STORE}
BASE="http://127.0.0.1:$PORT"
export TZ=${DAEMON_TZ:-America/Los_Angeles}

# c is curl with this daemon's access code (single-tenant: every caller,
# loopback included, presents it).
c() { curl -H "Authorization: Bearer $(cat "$WORK/data/local-api-token" 2>/dev/null)" "$@"; }

sql() {
	if [[ $STORE == sqlite ]]; then
		sqlite3 "$WORK/data/store/grain.db" "$1"
	else
		psql -q "$PG_URL" -c "$1"
	fi
}

start() {
	local args=(daemon -sandbox-port-base=0 -host-sandboxes -data-dir "$WORK/data"
		-sandbox-dir "$WORK/sandboxes" -ui-addr "127.0.0.1:$PORT" -as alice@example.com)
	[[ $STORE == postgres ]] && args+=(-postgres-url "$PG_URL")
	"$GRAIN" "${args[@]}" >>"$WORK/daemon.log" 2>&1 &
	echo $! >"$WORK/daemon.pid"
	for _ in $(seq 100); do
		c -fsS "$BASE/api/config" -o /dev/null 2>/dev/null && return
		sleep 0.2
	done
	echo "daemon did not come up" >&2
	tail -20 "$WORK/daemon.log" >&2
	exit 1
}

stop() {
	[[ -f $WORK/daemon.pid ]] || return 0
	local pid
	pid=$(cat "$WORK/daemon.pid")
	kill "$pid" 2>/dev/null || true
	for _ in $(seq 100); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
	rm -f "$WORK/daemon.pid"
}

put_pause() { # key until reason
	sql "DELETE FROM usage_limit_pause WHERE scope_key = '$1';"
	sql "INSERT INTO usage_limit_pause (scope_key, team, framework, until_at, reason, updated_at) VALUES ('$1', 'default', '$4', '$2', '$3', '$(date -u +%Y-%m-%dT%H:%M:%SZ)');"
}

etag() { c -sS -D - -o /dev/null "$BASE/api/config" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{print $2}'; }
cond() { c -sS -o /dev/null -w '%{http_code}' -H "If-None-Match: $1" "$BASE/api/config"; }
pause_json() { c -sS "$BASE/api/config" | jq -c '{tz: .timeZone, agentPause: (.agentPause // null | if . then {until, secondsRemaining, n: (.pauses // [] | length)} else null end)}'; }


# Subcommands for banner.mjs, which drives the browser while this script
# moves the daemon: setup (one pause, daemon left running), second,
# extend (each a restart, daemon left running), down.
case ${1:-walk} in
setup)
	stop; rm -rf "$WORK"; mkdir -p "$WORK/data" "$WORK/sandboxes"; start
	c -fsS -X PUT -H 'Content-Type: application/json' -d '{"timeZone":"Asia/Tokyo"}' "$BASE/api/settings" >/dev/null
	stop
	put_pause team:default "$(date -u -d '+90 min' +%Y-%m-%dT%H:%M:%SZ)" "claude: usage limit reached" claude
	start; exit 0 ;;
second)
	stop
	put_pause team:default:codex "$(date -u -d '+30 min' +%Y-%m-%dT%H:%M:%SZ)" "codex: usage limit reached" codex
	start; exit 0 ;;
extend)
	stop
	put_pause team:default "$(date -u -d '+150 min' +%Y-%m-%dT%H:%M:%SZ)" "claude: usage limit reached" claude
	start; exit 0 ;;
down) stop; exit 0 ;;
walk) ;;
*) echo "usage: $0 [walk|setup|second|extend|down]" >&2; exit 2 ;;
esac

stop
rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/sandboxes"
start
c -fsS -X PUT -H 'Content-Type: application/json' -d '{"timeZone":"Asia/Tokyo"}' "$BASE/api/settings" >/dev/null
stop

until1=$(date -u -d '+90 min' +%Y-%m-%dT%H:%M:%SZ)
put_pause team:default "$until1" "claude: usage limit reached" claude
start
echo "== one pause, TZ=$TZ"
pause_json
c -sS -i "$BASE/api/config" -o /dev/null -D - | tr -d '\r' | grep -iE '^(HTTP|etag|cache-control|vary)'
e1=$(etag)
sleep 3
echo "conditional after 3s with e1: $(cond "$e1") (want 304)"
pause_json

stop
put_pause team:default:codex "$(date -u -d '+30 min' +%Y-%m-%dT%H:%M:%SZ)" "codex: usage limit reached" codex
start
e2=$(etag)
echo "== second pause: e1->e2 $e1 -> $e2, conditional e1: $(cond "$e1") (want 200)"
pause_json

stop
put_pause team:default "$(date -u -d '+150 min' +%Y-%m-%dT%H:%M:%SZ)" "claude: usage limit reached" claude
start
e3=$(etag)
echo "== extension: e2->e3 $e2 -> $e3, conditional e2: $(cond "$e2") (want 200), e3: $(cond "$e3") (want 304)"
pause_json

echo "== end early (live DELETE /api/pause)"
c -sS -X DELETE "$BASE/api/pause" | jq -c '{lifted, paused: .pause.paused}' || true
e4=$(etag)
echo "after lift: e3->e4 $e3 -> $e4, conditional e3: $(cond "$e3") (want 200)"
pause_json
stop
