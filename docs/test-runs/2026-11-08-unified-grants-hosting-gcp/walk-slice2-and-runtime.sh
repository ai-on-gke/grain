#!/bin/sh
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

# walk-slice2-and-runtime.sh: runs Requirements 3, 4a, and 4b against a live
# `grain daemon -host-sandboxes` backed by `walksim`.
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
GRAIN_BIN=${GRAIN_BIN:-/home/debian/bin/grain}
WALKSIM_BIN=${WALKSIM_BIN:-/home/debian/bin/walksim}
W=$(mktemp -d /tmp/walk-slice2.XXXXXX)
SIM_PORT=14250
UI_PORT=17830

cleanup() {
  [ -n "${DPID:-}" ] && kill "$DPID" 2>/dev/null || true
  [ -n "${SPID:-}" ] && kill "$SPID" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

cp "$DIR/fake-agy.sh" "$W/fake-agy"
chmod +x "$W/fake-agy"

"$WALKSIM_BIN" "127.0.0.1:$SIM_PORT" "$W/repos" "$W/app-key.pem" >"$W/walksim.log" 2>&1 &
SPID=$!
sleep 1

export WALK_DIR="$W"
"$GRAIN_BIN" daemon \
  -data-dir "$W/data" \
  -ui-addr "127.0.0.1:$UI_PORT" \
  -sandbox-port-base=0 \
  -host-sandboxes \
  -sandbox-dir "$W/sandboxes" \
  -poll-interval 1s \
  -github-host "127.0.0.1:$SIM_PORT" \
  -github-insecure-http >"$W/daemon.log" 2>&1 &
DPID=$!

for _ in $(seq 60); do
  if [ -s "$W/data/local-api-token" ] && curl -fsS "http://127.0.0.1:$UI_PORT/api/config" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

python3 "$DIR/walk-slice2-and-runtime.py" \
  "http://127.0.0.1:$UI_PORT" \
  "http://127.0.0.1:$SIM_PORT" \
  "$W/data/local-api-token" \
  "$W" \
  "$W/app-key.pem"

echo ""
echo "==================== 7. Reaper Sweep on Restart (Leaked Sandbox Repo) ===================="
curl -fsS -X POST "http://127.0.0.1:$SIM_PORT/sim/seed-sandbox-repo?name=grain-sandbox-stale-walk&age=2h"
curl -fsS -X POST "http://127.0.0.1:$SIM_PORT/sim/seed-sandbox-repo?name=grain-sandbox-live-walk&age=2m"
curl -fsS -X POST "http://127.0.0.1:$SIM_PORT/sim/seed-sandbox-repo?name=unrelated-repo&age=2h"
echo "Before daemon restart:"
curl -fsS "http://127.0.0.1:$SIM_PORT/sim/sandbox-state"
echo ""

kill "$DPID" 2>/dev/null || true
wait "$DPID" 2>/dev/null || true
DPID=""

"$GRAIN_BIN" daemon \
  -data-dir "$W/data" \
  -ui-addr "127.0.0.1:$UI_PORT" \
  -sandbox-port-base=0 \
  -host-sandboxes \
  -sandbox-dir "$W/sandboxes" \
  -poll-interval 1s \
  -github-host "127.0.0.1:$SIM_PORT" \
  -github-insecure-http >"$W/daemon-restart.log" 2>&1 &
DPID=$!

for _ in $(seq 60); do
  if curl -fsS "http://127.0.0.1:$UI_PORT/api/config" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done
sleep 2
echo "After daemon restart (startup capability reaper):"
curl -fsS "http://127.0.0.1:$SIM_PORT/sim/sandbox-state"
echo ""
grep -E "reap|sandbox-repo|grain-sandbox" "$W/daemon-restart.log" || true
