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

# min-interval.sh walks the minimum interval between release-triggered
# upgrades (autoUpgradeMinIntervalHours) on a real image-mode daemon in
# about eight minutes, using the test-only debug clock: a grain built with
# -tags grain_debugclock reads GRAIN_DEBUG_CLOCK_OFFSET_FILE every tick
# and measures the 1h settle window and the 6h interval that far ahead
# of the real clock (pkg/daemon/debug_clock.go). It shows:
#
#   1. two releases inside the interval give one drain, onto the newer;
#   2. a failed upgrade starts no cooldown;
#   3. 6h holds a release that 0 then lets through at once.
#
# It needs docker and sqlite3, and runs a registry:2 on localhost:5000.
# Each release is followed by a daemon restart: the upgrader caches the
# registry's tag list for 10 minutes of real time, which the debug clock
# does not move.
set -euo pipefail
REPO=${REPO:-$(git rev-parse --show-toplevel)}
W=${W:-/tmp/min-interval-walk}
PORT=${PORT:-4291}
REG=localhost:5000/acme/grain
GRAIN=$W/grain

mkdir -p "$W/img"
(cd "$REPO" && go build -tags grain_debugclock -o "$GRAIN" ./cmd/grain)
cp "$GRAIN" "$W/img/grain"
docker inspect walk-registry >/dev/null 2>&1 || docker run -d --name walk-registry -p 5000:5000 registry:2 >/dev/null
printf 'FROM debian:bookworm-slim\nCOPY grain /usr/local/bin/grain\nENTRYPOINT ["/usr/local/bin/grain"]\n' >"$W/img/good"
printf 'FROM debian:bookworm-slim\nENTRYPOINT ["/bin/false"]\n' >"$W/img/broken"
echo 0 >"$W/clock"

# push TAG [good|broken]: a release whose digest differs from every other.
push() {
	{ cat "$W/img/${2:-good}"; echo "LABEL walk.release=$1"; } >"$W/img/Dockerfile.$1"
	docker build -q -f "$W/img/Dockerfile.$1" -t "$REG:$1" "$W/img" >/dev/null
	docker push -q "$REG:$1" >/dev/null
	echo "== pushed $1 (${2:-good})"
}
daemon() {
	pkill -f "^$GRAIN daemon" || true
	sleep 1
	GRAIN_DEBUG_CLOCK_OFFSET_FILE=$W/clock nohup "$GRAIN" daemon -data-dir "$W/data" -sandbox-port-base=0 \
		-disable-native-sandboxes -ui-addr "127.0.0.1:$PORT" -poll-interval 3s \
		-upgrade-image "$REG" -upgrade-image-ref-file "$W/image.env" \
		-upgrade-allowed-branches '**' -upgrade-grace-window 0 >>"$W/daemon.log" 2>&1 &
	sleep 15
}
clock() { echo "$1" >"$W/clock"; echo "== debug clock +$1"; sleep "${2:-30}"; }
record() { sqlite3 -readonly -header -column "$W/data/store/grain.db" 'select newest, seen_at, release_tag, outcome, succeeded_at from auto_upgrade_release'; }
settings() {
	curl -sf -H "Authorization: Bearer $(GRAIN_DATA_DIR=$W/data "$GRAIN" access-code | tail -1)" \
		-X PUT -H 'Content-Type: application/json' -d "$1" "127.0.0.1:$PORT/api/settings" |
		jq -c '{autoUpgradeOn, autoUpgradeMinIntervalHours}'
}

daemon
settings '{"autoUpgradeOn":"release","autoUpgradeChannel":"","autoUpgradeMinIntervalHours":6}'

push v9.0.0 && daemon && clock 1h && record          # upgrades onto v9.0.0
push v9.0.1 && daemon && clock 2h5m 15               # settled, inside 6h: held
push v9.0.2 && daemon && clock 3h10m && record       # settled, inside 6h: held
clock 7h30m && record                                # one drain, onto v9.0.2

push v9.0.3 broken && daemon && clock 13h35m 45 && record # fails its health check
push v9.0.4 && daemon && clock 14h40m && record      # 1h after the failure: goes

push v9.0.5 && daemon && clock 15h45m 20 && record   # 1h after v9.0.4: held by 6h
settings '{"autoUpgradeMinIntervalHours":0}'
sleep 25 && record                                   # 0: goes at once

grep -E 'orchestrator: (release|the automatic)|debug clock|upgrade: v9\.0\.[0-9]: (starting|succeeded|failed)' "$W/daemon.log"
pkill -f "^$GRAIN daemon" || true
