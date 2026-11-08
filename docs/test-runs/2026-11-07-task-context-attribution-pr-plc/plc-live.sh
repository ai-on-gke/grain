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

# Item 5 of docs/test-plans/2026-11-07.md §7: plc-studio and plc-chat-gateway
# against a live local grain server (grain demo -github-sim -human-review),
# filing and polling a task with externalRef and committed context, and the
# gateway consuming GET /api/events.
#
#   GRAIN_BIN=/home/debian/bin/grain PLC_BIN=/home/debian/bin \
#     docs/test-runs/2026-11-07-task-context-attribution-pr-plc/plc-live.sh
set -u
GRAIN_BIN=${GRAIN_BIN:-/home/debian/bin/grain}
PLC_BIN=${PLC_BIN:-/home/debian/bin}
OUT=${OUT:-$(cd "$(dirname "$0")" && pwd)}
WORK=$(mktemp -d)
GRAIN_ADDR=127.0.0.1:4292
STUDIO=127.0.0.1:4287
GATEWAY=127.0.0.1:4288
LOG="$OUT/item5-plc-live.log"
: >"$LOG"
say() { echo "$*" | tee -a "$LOG"; }
pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null; done; rm -rf "$WORK"; }
trap cleanup EXIT

"$GRAIN_BIN" demo -github-sim -human-review -addr "$GRAIN_ADDR" >"$WORK/demo.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 60); do
  grep -q 'serving fake tasks' "$WORK/demo.log" 2>/dev/null && break
  sleep 0.5
done
TOKEN_FILE=$(grep -o '/tmp/grain-demo-[^ )]*/local-api-token' "$WORK/demo.log" | head -1)
TOKEN=$(cat "$TOKEN_FILE")
say "grain demo up at http://$GRAIN_ADDR (access code from $TOKEN_FILE)"

start_plc() {
  local token=$1
  GRAIN_TOKEN=$token "$PLC_BIN/plc-studio" -listen "$STUDIO" -grain-url "http://$GRAIN_ADDR" \
    -default-user plc@example.com >"$WORK/studio.log" 2>&1 &
  pids+=($!)
  GRAIN_TOKEN=$token "$PLC_BIN/plc-chat-gateway" -listen "$GATEWAY" -grain-url "http://$GRAIN_ADDR" \
    -watch-sse >"$WORK/gateway.log" 2>&1 &
  pids+=($!)
  sleep 1.5
}

say "== 5a without a token (the only mode before this fix): studio's upstream call"
start_plc ""
say "$(curl -s -X POST "http://$STUDIO/api/studio/tasks" -H 'Content-Type: application/json' \
  -d '{"title":"PLC filed","body":"x","repo":"acme/widgets","externalRef":"PLC-7"}')"
sleep 1
say "gateway stderr without a token:"
say "$(head -n 3 "$WORK/gateway.log")"
for p in "${pids[@]:1}"; do kill "$p" 2>/dev/null; done
pids=("${pids[0]}")
sleep 0.5

say "== 5b with -grain-token / GRAIN_TOKEN"
start_plc "$TOKEN"
say "-- POST /api/studio/tasks (externalRef + committed context)"
CREATED=$(curl -s -X POST "http://$STUDIO/api/studio/tasks" -H 'Content-Type: application/json' \
  -d '{"title":"PLC-7 implement billing","body":"Implement the billing spec.","repo":"acme/widgets","externalRef":"PLC-7","context":[{"path":".grain/context/planner/plc-042"}]}')
say "$CREATED"
ID=$(echo "$CREATED" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -1)
say "-- GET /api/studio/tasks?externalRef=PLC-7"
say "$(curl -s "http://$STUDIO/api/studio/tasks?externalRef=PLC-7" | head -c 1500)"
say "-- GET /api/studio/refs/PLC-7/summary"
say "$(curl -s "http://$STUDIO/api/studio/refs/PLC-7/summary")"
say "-- grain show $ID (what the daemon stored)"
say "$(GRAIN_SERVER=http://$GRAIN_ADDR GRAIN_TOKEN=$TOKEN "$GRAIN_BIN" show "$ID" 2>&1 | head -20)"
say "-- REST payload fields the studio wrote"
say "$(curl -s -H "Authorization: Bearer $TOKEN" "http://$GRAIN_ADDR/api/tasks/$ID" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(json.dumps({k:d.get(k) for k in ("id","description","externalRef","context","designs","filedVia","origin")}))')"
say "-- approve $ID so grain emits a task.state event"
say "$(GRAIN_SERVER=http://$GRAIN_ADDR GRAIN_TOKEN=$TOKEN "$GRAIN_BIN" approve "$ID" 2>&1 | head -3)"
sleep 2
say "-- GET /api/events?externalRef=PLC-7 (JSON poll, what the gateway's SSE stream carries)"
say "$(curl -s -H "Authorization: Bearer $TOKEN" "http://$GRAIN_ADDR/api/events?externalRef=PLC-7&limit=10" | head -c 1500)"
say "gateway stderr with the token (empty = stream connected, nothing refused):"
say "$(cat "$WORK/gateway.log")"
say "gateway perf (caches.sse.events_received counts events the SSE consumer decoded):"
say "$(curl -s "http://$GATEWAY/api/chat/perf" | head -c 600)"
say "studio perf:"
say "$(curl -s "http://$STUDIO/api/studio/perf" | head -c 800)"
