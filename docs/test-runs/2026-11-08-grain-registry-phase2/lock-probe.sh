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

# §1 migration locking: hold the registry's migrate pg_advisory_lock from a
# psql session, start two fresh replicas against an empty database at once,
# and show neither migrates (or answers /healthz) until the lock is released,
# after which the schema is at 7 with every migration applied exactly once.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/lib.sh"
KEY=$((0x67726e726567)) # migrateLockKey in pkg/registry/store/migrations.go
DB=registry_lockprobe
psql -h 127.0.0.1 -p 4240 -U postgres -qc "DROP DATABASE IF EXISTS $DB" -c "CREATE DATABASE $DB"
psql -h 127.0.0.1 -p 4240 -U postgres $DB -qtAc "SELECT pg_advisory_lock($KEY); SELECT pg_sleep(4); SELECT 'lock released at ' || clock_timestamp()::time" >"$WALK/lockprobe-holder.out" &
HOLDER=$!
sleep 0.5
pids=()
for r in x y; do
  if [ $r = x ]; then base=4292; else base=4296; fi
  sed -e "s/127.0.0.1:4252/127.0.0.1:$base/; s/127.0.0.1:4253/127.0.0.1:$((base + 1))/; s/127.0.0.1:4254/127.0.0.1:$((base + 2))/" \
    -e "s#/registry?sslmode#/$DB?sslmode#" "$WALK/registry-a.yaml" >"$WALK/registry-$r.yaml"
  "$G-registry" serve -config "$WALK/registry-$r.yaml" >"$WALK/logs/lockprobe-$r.log" 2>&1 &
  pids+=($!)
done
T0=$(date +%s%N)
for t in 1 2 3; do
  sleep 1
  echo "t+${t}s: healthz x=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:4293/healthz) y=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:4297/healthz)" \
    "waiting on advisory lock: $(psql -h 127.0.0.1 -p 4240 -U postgres $DB -tAc "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted")"
done
wait $HOLDER
cat "$WALK/lockprobe-holder.out" | grep released
for _ in $(seq 1 50); do
  curl -fs -o /dev/null http://127.0.0.1:4293/healthz && curl -fs -o /dev/null http://127.0.0.1:4297/healthz && break
  sleep 0.1
done
echo "both healthy after $((($(date +%s%N) - T0) / 1000000))ms"
echo "schema_migrations: $(psql -h 127.0.0.1 -p 4240 -U postgres $DB -tAc "SELECT count(*) || ' rows, ' || count(DISTINCT version) || ' distinct, max ' || max(version) FROM registry.schema_migrations")"
echo "replica logs:"
cat "$WALK/logs/lockprobe-x.log" "$WALK/logs/lockprobe-y.log" | sed 's/^/    /'
kill "${pids[@]}"
