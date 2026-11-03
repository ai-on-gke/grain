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

# Item 4: restart the daemon while a run's CLI is live on ws-a, and
# record what the operator sees: is the run lost, re-queued, reported,
# or re-adopted from ws-a?
set -uo pipefail
cd "$(dirname "$0")" || exit
. ./env.sh
. ./lib.sh
set +e
# Lift item 6's ws-a pause the way Settings -> Models does.
api DELETE /api/pause '{"connection":"agy-ambient@hostd/ws-a","model":"fake-agy-model"}' >/dev/null
api GET /api/pause | jq -c '[.pause.pauses[]? | .connection]'
R=$ROOT/reports/item4.json
rm -f "$R" "$R.pid"
id=$(file "item4: on ws-a across a daemon restart" ws-a "report=$R mode=steady ticks=50" | task_id)
echo "task $id"
for _ in $(seq 120); do [ -s "$R.pid" ] && break; sleep 0.5; done
pid=$(cat "$R.pid")
echo "$(date -u +%T) CLI pid $pid is live under: $(ps -o ppid= -p "$pid") (ws-a's hostd is $(cat "$ROOT/ws-a/pid"))"
api GET "/api/tasks/$id" | jq -c '{state, attempts: [.attempts[]? | {outcome, hostd: .hostd.hostd}]}'
echo "$(date -u +%T) stopping the daemon"
daemon_down
sleep 2
printf '%s CLI still alive while the daemon is down: ' "$(date -u +%T)"; kill -0 "$pid" 2>/dev/null && echo yes || echo no
"$BIN/grain" hostd -status -socket "$ROOT/ws-a/hostd.sock" 2>&1 | head -4
echo "$(date -u +%T) starting the daemon"
daemon_up
last=""
for _ in $(seq 90); do
  s=$(api GET "/api/tasks/$id" | jq -c '{state, activity, attempts: [.attempts[]? | {outcome, detail: (.detail // "" | .[0:160]), hostd: .hostd.hostd}]}')
  [ "$s" != "$last" ] && echo "$(date -u +%T) $s" && last=$s
  echo "$s" | grep -q '"outcome":"succeeded"' && break
  sleep 1
done
printf 'CLI after restart: '; kill -0 "$pid" 2>/dev/null && echo "still alive" || echo "gone"
echo "--- daemon log about run $id-1"
grep -E "run $id-1|task $id\b|adopt|re-adopt|interrupted" "$LOG/daemon.log" | tail -12 | cut -c1-260
echo "--- records"
records "$id"
