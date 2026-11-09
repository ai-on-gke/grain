#!/bin/bash
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

# walk.sh <name> <old-binary> <legacy|canonical> <sqlite|postgres>: §2
# items 1-2 for one old build and one store kind.
#   seed      the old daemon makes the store; seed.sh writes the grants
#   in place  HEAD starts on a copy (old schema -> SchemaVersion), its log,
#             a grant dump and the API's view are captured, then
#             `grain state sync` + `grain state check` on its own export
#   dump      `grain state prepare-upgrade` on a second copy (export the old
#             rows, rehearse the import), then HEAD starts on it
#   downgrade the old daemon is started on the upgraded in-place copy; it
#             must refuse, and the store must be unchanged afterwards
# Outputs land in $OUT (default: ./out) as <name>-*.txt.
set -u
name=$1 OLD=$2 style=$3 kind=$4
here=$(cd "$(dirname "$0")" && pwd)
OUT=${OUT:-$here/out}
GHEAD=${GHEAD:-/home/debian/bin/grain}
W=/home/debian/walk2/$name
mkdir -p "$OUT"
pkill -f "walk2/$name/" ; sleep 1
rm -rf "$W" && mkdir -p "$W"
pg() { psql -X -q "postgres://debian:walk@127.0.0.1:5432/postgres" -c "$1"; }
dbname() { echo "${name//-/_}_$1"; }
url() { echo "postgres://debian:walk@127.0.0.1:5432/$1?sslmode=disable"; }
store_flags() { if [ "$kind" = postgres ]; then echo "-postgres-url $(url "$(dbname "$1")")"; fi; }
db_of() { if [ "$kind" = postgres ]; then url "$(dbname "$1")"; else echo "$W/$1/store/grain.db"; fi; }
copy() { # copy <from> <to>
  cp -a "$W/$1" "$W/$2"
  rm -f "$W/$2/db-config.json"
  if [ "$kind" = postgres ]; then
    pg "DROP DATABASE IF EXISTS $(dbname "$2")"
    pg "CREATE DATABASE $(dbname "$2") TEMPLATE $(dbname "$1")"
  fi
}
stop() { kill "$(cat "$W/$1.pid")" 2>/dev/null; for _ in $(seq 30); do kill -0 "$(cat "$W/$1.pid")" 2>/dev/null || return 0; sleep 1; done; }
api() { # api <copy> <port> <path>
  curl -sS -H "Authorization: Bearer $(cat "$W/$1/local-api-token")" "http://127.0.0.1:$2$3"
}
if [ "$kind" = postgres ]; then
  for c in seed inplace prepared; do pg "DROP DATABASE IF EXISTS $(dbname $c)"; done
  pg "CREATE DATABASE $(dbname seed)"
fi

echo "== seed ($OLD)"
"$here/start.sh" "$OLD" "$W/seed" 4221 $(store_flags seed) || exit 1
"$here/seed.sh" http://127.0.0.1:4221 "$W/seed/local-api-token" "$(db_of seed)" "$(cat "$W/seed.pid")" "$style" >"$OUT/$name-seed.txt" 2>&1
"$here/dump.sh" "$(db_of seed)" >"$OUT/$name-grants-old.txt" 2>&1

echo "== in place"
copy seed inplace
"$here/start.sh" "$GHEAD" "$W/inplace" 4222 $(store_flags inplace) || exit 1
sleep 8
grep -E 'migrat|catalogue|grant wait|schema' "$W/inplace.log" | cut -c21- >"$OUT/$name-inplace-log.txt"
"$here/dump.sh" "$(db_of inplace)" >"$OUT/$name-grants-head-inplace.txt" 2>&1
t2=$(grep -o 'task=[0-9]*,[0-9]*' "$OUT/$name-seed.txt" | cut -d, -f2)
{
  echo "# GET /api/settings defaultCapabilities"; api inplace 4222 /api/settings | jq -c '.defaultCapabilities'
  echo "# GET /api/capability-policy catalogue.defaults"; api inplace 4222 /api/capability-policy | jq -c '.catalogue.defaults // .policy.catalogue.defaults'
  echo "# GET /api/repos/acme/widgets capabilities"; api inplace 4222 /api/repos/acme/widgets/settings | jq -c '{defaultCapabilities, ciCapabilities}'
  echo "# GET /api/templates"; api inplace 4222 /api/templates | jq -c '.[] | {id, capabilities, description}'
  echo "# GET /api/schedules"; api inplace 4222 /api/schedules | jq -c '.[] | select(.id | startswith("system:") | not) | {id, capabilities, description}'
  echo "# GET /api/tasks/$t2 (grant wait)"; api inplace 4222 "/api/tasks/$t2" | jq -c '{id, state, capabilities, activity}'
} >"$OUT/$name-api.txt" 2>&1
stop inplace
"$GHEAD" state -data-dir "$W/inplace" sync >"$W/sync.log" 2>&1
{ "$GHEAD" state check "$W/inplace/state-repo" 2>&1 && echo "state check: ok"; } | tail -3 >"$OUT/$name-state-check.txt"

echo "== dump route"
copy seed prepared
penv=()
[ "$kind" = postgres ] && penv=(GRAIN_DB_DRIVER=postgres "GRAIN_DB_DSN=$(db_of prepared)")
env "${penv[@]}" "$GHEAD" state -data-dir "$W/prepared" prepare-upgrade 2>&1 | tail -3 >"$OUT/$name-prepare-upgrade.txt"
if grep -q '^prepared' "$OUT/$name-prepare-upgrade.txt"; then
  "$here/start.sh" "$GHEAD" "$W/prepared" 4223 $(store_flags prepared) || exit 1
  sleep 8
  grep -E 'migrat|catalogue|import' "$W/prepared.log" | cut -c21- >"$OUT/$name-prepared-log.txt"
  "$here/dump.sh" "$(db_of prepared)" >"$OUT/$name-grants-head-dump-route.txt" 2>&1
  stop prepared
  diff "$OUT/$name-grants-head-inplace.txt" "$OUT/$name-grants-head-dump-route.txt" >"$OUT/$name-inplace-vs-dump-route.diff"
fi
diff "$OUT/$name-grants-old.txt" "$OUT/$name-grants-head-inplace.txt" >"$OUT/$name-old-vs-head-inplace.diff"

echo "== downgrade"
cp "$W/inplace.log" "$W/inplace-head.log"
"$here/dump.sh" "$(db_of inplace)" >"$W/before-downgrade.txt" 2>&1
"$here/start.sh" "$OLD" "$W/inplace" 4224 $(store_flags inplace) >/dev/null 2>&1
sleep 3
stop inplace
grep -iE 'newer|refus|knows' "$W/inplace.log" | cut -c21- | head -5 >"$OUT/$name-downgrade.txt"
"$here/dump.sh" "$(db_of inplace)" >"$W/after-downgrade.txt" 2>&1
if diff -q "$W/before-downgrade.txt" "$W/after-downgrade.txt" >/dev/null; then
  echo "store unchanged by the refused start" >>"$OUT/$name-downgrade.txt"
else
  diff "$W/before-downgrade.txt" "$W/after-downgrade.txt" >>"$OUT/$name-downgrade.txt"
fi
echo "done: $(ls "$OUT"/$name-* | wc -l) files"
