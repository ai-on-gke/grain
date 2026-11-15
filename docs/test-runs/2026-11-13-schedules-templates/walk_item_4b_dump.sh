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

# walk_item_4b_dump.sh is the dump/import half of §8 item 4: schedule
# skip_when (schema 141) and template sandbox (schema 143) through the
# state repository and back. Daemon A (env.sh, DB=sqlite|postgres) loads
# acme/widgets' declarations (big.yml's sandbox, held.yml's skip-when)
# and an operator-written template with sandbox: {cpus: 4, disk-gb: 80}
# over the API; its own export writes them to data/state-repo. Daemon B starts on a fresh store (SQLite, or a second
# PostgreSQL database) whose state repository is a clone of A's, and
# imports them on its first start.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
DB=${DB:-sqlite}
export DB WORK_ROOT=/var/tmp/walk-sched-dump-$DB SIM_PORT=4274 UI_PORT=4275 API_URL=http://127.0.0.1:4275 PG_DB=walk_sched_dump
GRAIN=${GRAIN_BIN:-/home/debian/bin/grain}

echo "=== daemon A ($DB)"
"$HERE/env.sh"
# shellcheck source=/dev/null
source "$HERE/helpers.sh"
wait_sync "SELECT count(*) FROM schedule WHERE id LIKE 'repo:schedule:acme:widgets:%'" 4
TMPL=$(api POST /api/templates '{"name":"Operator big","title":"Operator big build","body":"Big.","sandbox":{"cpus":4,"diskGb":80}}')
echo "$TMPL" | jq -c '{id, sandbox}'
# The API takes no skip-when (F5): the repository's held.yml is the
# schedule that carries it through the dump.
sleep 5
D="$WORK_ROOT/data/state-repo"
echo "--- A's dump: settings/templates.yml and settings/schedules.yml"
grep -n -B1 -A0 "sandbox: {" "$D/settings/templates.yml" | grep -v "^--$"
grep -n "skip-when: publishing-held" "$D/settings/schedules.yml"
db "SELECT id, sandbox FROM template WHERE sandbox <> '' ORDER BY id"
db "SELECT id, skip_when FROM schedule WHERE skip_when <> '' ORDER BY id"
echo "--- grain state check on A's dump"
"$GRAIN" state check "$D" 2>&1 | tail -n 3

echo "=== daemon B: a fresh store whose state repository is a clone of A's"
kill "$(cat "$WORK_ROOT/daemon.pid")"; sleep 2
B="$WORK_ROOT/b"
mkdir -p "$B/data" "$B/sb"
git clone -q "$D" "$B/data/state-repo"
B_FLAGS=()
if [ "$DB" = postgres ]; then
  PGPASSWORD=grain psql -h 127.0.0.1 -U grain -d postgres -qc "DROP DATABASE IF EXISTS walk_sched_dump_b" -c "CREATE DATABASE walk_sched_dump_b"
  B_FLAGS=(-postgres-url "postgres://grain:grain@127.0.0.1:5432/walk_sched_dump_b?sslmode=disable")
fi
setsid "$GRAIN" daemon -data-dir "$B/data" -ui-addr 127.0.0.1:4276 "${B_FLAGS[@]}" -sandbox-port-base=0 \
  -host-sandboxes -sandbox-dir "$B/sb" -reconcile -poll-interval 1s >"$B/daemon.log" 2>&1 </dev/null &
echo $! >"$B/daemon.pid"
for _ in $(seq 120); do [ -s "$B/data/local-api-token" ] && curl -fsS -H "Authorization: Bearer $(cat "$B/data/local-api-token")" http://127.0.0.1:4276/api/config >/dev/null 2>&1 && break; sleep 0.5; done
grep -iE "import|state repo" "$B/daemon.log" | head -n 5 | cut -c1-300
# shellcheck disable=SC2034 # read by db() in helpers.sh
if [ "$DB" = postgres ]; then PG_DB=walk_sched_dump_b; else SQLITE_DB="$B/data/store/grain.db"; fi
db "SELECT id, sandbox FROM template WHERE sandbox <> '' ORDER BY id"
db "SELECT id, skip_when FROM schedule WHERE skip_when <> '' ORDER BY id"
BT=$(cat "$B/data/local-api-token")
curl -sS -H "Authorization: Bearer $BT" http://127.0.0.1:4276/api/templates | jq -c '[.[]? // .templates[]? | select(.sandbox != null) | {id, sandbox}]'
kill "$(cat "$B/daemon.pid")" "$(cat "$WORK_ROOT/sim.pid")" 2>/dev/null || true
