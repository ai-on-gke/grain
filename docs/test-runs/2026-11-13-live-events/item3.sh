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

# item3.sh <sqlite|postgres> <with|without> [port]: §3 item 3, the 139 -> 140
# upgrade (HEAD is at 146, so the daemon walks 139 -> 146).
#
# GRAIN139 is a grain built from 1d053240^1, the last main commit at schema
# 139 (`git worktree add ../wt139 1d053240^1 && go build ./cmd/grain`). It
# makes the store: a real daemon started and stopped (its own startup writes
# task.* rows into integration_event). "without" then drops integration_event, as a store
# from before that table existed (it came in with no rung of its own) has
# none. HEAD's daemon then opens the store; the script prints the stamped
# version, integration_event's columns, the rows that survived, and the
# notification.raised a raise records afterwards.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=common.sh
source "$HERE/common.sh"
kind=${1:-sqlite} variant=${2:-with} port=${3:-4300}
GRAIN139=${GRAIN139:-/home/debian/bin/grain139}
W=/tmp/wl-item3-$kind-$variant
OUT=${OUT:-$HERE/out/item3-$kind-$variant}
mkdir -p "$OUT"
reset_store "$kind" "$W"

cols() {
	if [[ $kind == postgres ]]; then
		sql "$kind" "$W" "SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name='integration_event'"
	else
		sql "$kind" "$W" "SELECT group_concat(name, ',') FROM pragma_table_info('integration_event')"
	fi
}

{
	echo "== 139: $("$GRAIN139" version 2>/dev/null | head -n 1)"
	start_daemon "$kind" "$W" "$port" "$GRAIN139" || exit 1
	sleep 1
	stop_daemon
	echo "version: $(sql "$kind" "$W" 'SELECT version FROM grain_schema WHERE id = 1')"
	echo "integration_event columns: $(cols)"
	echo "integration_event rows: $(sql "$kind" "$W" 'SELECT count(*) FROM integration_event')"
	if [[ $variant == without ]]; then
		sql "$kind" "$W" 'DROP TABLE integration_event'
		echo "dropped integration_event; columns now: '$(cols)'"
	fi

	echo "== HEAD: $("$GRAIN" version 2>/dev/null | head -n 1)"
	start_daemon "$kind" "$W" "$port" || { tail -n 20 "$W/daemon.log"; exit 1; }
	echo "version: $(sql "$kind" "$W" 'SELECT version FROM grain_schema WHERE id = 1')"
	echo "integration_event columns: $(cols)"
	echo "rows before any new write: $(sql "$kind" "$W" 'SELECT count(*) FROM integration_event')"
	api POST /api/notifications '{"subject":"walk.upgrade","title":"after upgrade","severity":"warning"}' >/dev/null
	echo "events since 0:"
	api GET '/api/events?since=0&limit=100' | jq -r '.events[] | "  \(.id) \(.type) \(.notificationId // "") \(.severity // "")"'
	echo "migration lines in daemon.log:"
	grep -i 'migrat' "$W/daemon.log" | sed 's/^/  /' | tail -n 8
	stop_daemon
} 2>&1 | tee "$OUT/summary.txt"
