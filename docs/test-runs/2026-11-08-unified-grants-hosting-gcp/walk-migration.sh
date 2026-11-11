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

# walk-migration.sh <sqlite|postgres>: executes §3 item 1 (schema 122 -> HEAD
# migration, prepare-upgrade, bare-remote export, state check, fresh Postgres
# re-import, legacy deprecation warnings, and #851 merged-catalogue validation).
set -euo pipefail

kind=$1
here=$(cd "$(dirname "$0")" && pwd)
OUT=${OUT:-/tmp/walk-migration-out}
mkdir -p "$OUT"
G122=${G122:-/home/debian/bin/grain122}
GHEAD=${GHEAD:-/home/debian/bin/grain}
W=/tmp/walk122/$kind
rm -rf "$W" && mkdir -p "$W"

pg_admin() { psql -X -q "postgres://debian:walk@127.0.0.1:5432/postgres?sslmode=disable" -c "$1"; }
pg_url() { echo "postgres://debian:walk@127.0.0.1:5432/$1?sslmode=disable"; }

start_daemon() {
  local bin=$1 dir=$2 port=$3
  shift 3
  mkdir -p "$dir"
  setsid "$bin" daemon -data-dir "$dir" -ui-addr "127.0.0.1:$port" \
    -disable-native-sandboxes -sandbox-port-base=0 "$@" \
    >"$dir.log" 2>&1 </dev/null &
  local pid=$!
  echo "$pid" >"$dir.pid"
  for _ in $(seq 80); do
    if [ -s "$dir/local-api-token" ] && curl -fsS -o /dev/null \
      -H "Authorization: Bearer $(cat "$dir/local-api-token")" \
      "http://127.0.0.1:$port/api/config" 2>/dev/null; then
      return 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "daemon exited early on $port:" >&2
      tail -n 40 "$dir.log" >&2
      return 1
    fi
    sleep 0.5
  done
  echo "daemon timed out on $port:" >&2
  tail -n 40 "$dir.log" >&2
  return 1
}

stop_daemon() {
  local dir=$1
  if [ -f "$dir.pid" ]; then
    local pid
    pid=$(cat "$dir.pid")
    kill "$pid" 2>/dev/null || true
    for _ in $(seq 40); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.2
    done
  fi
}

store_flags() {
  local slot=$1
  if [ "$kind" = "postgres" ]; then
    echo "-postgres-url $(pg_url "walk122_${kind}_${slot}")"
  fi
}

db_of() {
  local slot=$1
  if [ "$kind" = "postgres" ]; then
    pg_url "walk122_${kind}_${slot}"
  else
    echo "$W/$slot/store/grain.db"
  fi
}

copy_slot() {
  local src=$1 dst=$2
  cp -a "$W/$src" "$W/$dst"
  rm -f "$W/$dst/db-config.json"
  if [ "$kind" = "postgres" ]; then
    pg_admin "DROP DATABASE IF EXISTS walk122_${kind}_${dst}"
    pg_admin "CREATE DATABASE walk122_${kind}_${dst} TEMPLATE walk122_${kind}_${src}"
  fi
}

if [ "$kind" = "postgres" ]; then
  for slot in seed inplace prepared; do
    pg_admin "DROP DATABASE IF EXISTS walk122_${kind}_${slot}"
  done
  pg_admin "CREATE DATABASE walk122_${kind}_seed"
fi
pg_admin "DROP DATABASE IF EXISTS walk122_${kind}_reimported"
pg_admin "CREATE DATABASE walk122_${kind}_reimported"

echo "=== [$kind] 1. Seed schema-122 store ==="
# shellcheck disable=SC2046
start_daemon "$G122" "$W/seed" 4211 $(store_flags seed)
"$here/seed122.sh" "http://127.0.0.1:4211" "$W/seed/local-api-token" "$(db_of seed)" "$(cat "$W/seed.pid")" >"$OUT/$kind-seed.txt"
"$here/dump-state122.sh" "$(db_of seed)" >"$OUT/$kind-dump-122.txt"

echo "=== [$kind] 2. grain state prepare-upgrade + boot HEAD ==="
copy_slot seed prepared
penv=()
if [ "$kind" = "postgres" ]; then
  penv=(GRAIN_DB_DRIVER=postgres "GRAIN_DB_DSN=$(db_of prepared)")
fi
env "${penv[@]}" "$GHEAD" state -data-dir "$W/prepared" prepare-upgrade >"$OUT/$kind-prepare-upgrade.txt" 2>&1
cat "$OUT/$kind-prepare-upgrade.txt"
# shellcheck disable=SC2046
start_daemon "$GHEAD" "$W/prepared" 4212 $(store_flags prepared)
sleep 2
"$here/dump-state122.sh" "$(db_of prepared)" >"$OUT/$kind-dump-prepared.txt"
stop_daemon "$W/prepared"

echo "=== [$kind] 3. Direct in-place migration + export to bare git remote + state check ==="
copy_slot seed inplace
# shellcheck disable=SC2046
start_daemon "$GHEAD" "$W/inplace" 4213 $(store_flags inplace)
sleep 2
"$here/dump-state122.sh" "$(db_of inplace)" >"$OUT/$kind-dump-inplace.txt"

# Probe GET /api/tasks to inspect how migrated tasks and grant waits render!
tok=$(cat "$W/inplace/local-api-token")
curl -fsS -H "Authorization: Bearer $tok" "http://127.0.0.1:4213/api/tasks" >"$OUT/$kind-tasks-after-migrate.json"
curl -fsS -H "Authorization: Bearer $tok" "http://127.0.0.1:4213/api/notifications" >"$OUT/$kind-notifications.json"
"$here/probe-migration-api.sh" "http://127.0.0.1:4213" "$W/inplace/local-api-token" "$GHEAD" >"$OUT/$kind-probe-api.txt" 2>&1
stop_daemon "$W/inplace"

grep -E 'migrat|deprecation|unconfigured-set|hosting' "$W/inplace.log" >"$OUT/$kind-inplace-log.txt" || true

# Export full state-repo dump and push to a bare git remote, then run grain state check
penv_inplace=()
if [ "$kind" = "postgres" ]; then
  penv_inplace=(GRAIN_DB_DRIVER=postgres "GRAIN_DB_DSN=$(db_of inplace)")
fi
env "${penv_inplace[@]}" "$GHEAD" state -data-dir "$W/inplace" sync >"$OUT/$kind-state-sync.txt" 2>&1
git init --bare --initial-branch=main "$W/remote.git" >/dev/null
git -C "$W/inplace/state-repo" push "$W/remote.git" main:main >>"$OUT/$kind-state-sync.txt" 2>&1
git clone -q "$W/remote.git" "$W/exported-clone"
{ "$GHEAD" state check "$W/exported-clone" 2>&1 && echo "state check: ok"; } >"$OUT/$kind-state-check.txt"
cat "$OUT/$kind-state-check.txt"

echo "=== [$kind] 4. Fresh PostgreSQL daemon importing bare git remote ==="
mkdir -p "$W/reimported"
key_src=$("$GHEAD" state -data-dir "$W/inplace" key path)
"$GHEAD" state -data-dir "$W/reimported" key import -key-file "$key_src" >/dev/null
cat >"$W/reimported/state-repo.json" <<EOF
{"remote":"$W/remote.git","branch":"main","mode":"mirror"}
EOF
start_daemon "$GHEAD" "$W/reimported" 4214 -postgres-url "$(pg_url "walk122_${kind}_reimported")"
sleep 2
"$here/dump-state122.sh" "$(pg_url "walk122_${kind}_reimported")" >"$OUT/$kind-dump-reimported-pg.txt"
stop_daemon "$W/reimported"

echo "=== [$kind] 5. Diffs ==="
diff -u "$OUT/$kind-dump-inplace.txt" "$OUT/$kind-dump-prepared.txt" >"$OUT/$kind-diff-inplace-vs-prepared.txt" || true
echo "inplace vs prepared diff lines: $(wc -l <"$OUT/$kind-diff-inplace-vs-prepared.txt")"
