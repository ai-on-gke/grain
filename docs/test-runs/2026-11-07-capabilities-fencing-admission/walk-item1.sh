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

# walk-item1.sh <name> <sqlite|postgres>: §6 item 1 end to end for one
# store kind. Seeds a schema-108 store with grain108 (cc66a6bc), then
#   - in place: starts the HEAD daemon on a copy (migrations 109..HEAD),
#     dumps every grant surface and probes the policy through the API;
#   - dump route: `grain state prepare-upgrade` on a second copy (export
#     the 108 rows, rehearse the import), then starts HEAD on it;
#   - round trip: `grain state check` on the upgraded store's own
#     state repository.
# Outputs land in $OUT (default: this directory) as <name>-*.txt.
set -u
name=$1 kind=$2
here=$(cd "$(dirname "$0")" && pwd)
OUT=${OUT:-$here}
G108=${G108:-/home/debian/bin/grain108}
GHEAD=${GHEAD:-/home/debian/bin/grain-head}
W=/home/debian/walk1/$name
pkill -x grain108; pkill -x grain-head; sleep 2
rm -rf "$W" && mkdir -p "$W"
pg() { psql -X -q "postgres://debian:walk@127.0.0.1:5432/postgres" -c "$1"; }
url() { echo "postgres://debian:walk@127.0.0.1:5432/$1?sslmode=disable"; }
store_flags() { # store_flags <copy>
  if [ "$kind" = postgres ]; then echo "-postgres-url $(url "${name//-/_}_$1")"; fi
}
db_of() { # db_of <copy>
  if [ "$kind" = postgres ]; then url "${name//-/_}_$1"; else echo "$W/$1/store/grain.db"; fi
}
copy() { # copy <from> <to>
  cp -a "$W/$1" "$W/$2"
  rm -f "$W/$2/db-config.json" # names the database it was copied from
  if [ "$kind" = postgres ]; then
    pg "DROP DATABASE IF EXISTS ${name//-/_}_$2"
    pg "CREATE DATABASE ${name//-/_}_$2 TEMPLATE ${name//-/_}_$1"
  fi
}
if [ "$kind" = postgres ]; then
  for c in seed inplace prepared; do pg "DROP DATABASE IF EXISTS ${name//-/_}_$c"; done
  pg "CREATE DATABASE ${name//-/_}_seed"
fi

echo "== seed (schema 108)"
"$here/start108.sh" "$G108" "$W/seed" 4211 $(store_flags seed) || exit 1
"$here/seed108.sh" http://127.0.0.1:4211 "$W/seed/local-api-token" "$(db_of seed)" "$(cat "$W/seed.pid")" | cut -c1-160 >"$OUT/$name-seed.txt"
"$here/dump-grants.sh" "$(db_of seed)" >"$OUT/$name-grants-108.txt"

echo "== in place"
copy seed inplace
"$here/start108.sh" "$GHEAD" "$W/inplace" 4212 $(store_flags inplace) || exit 1
sleep 5
grep -E 'migrat|deprecation' "$W/inplace.log" | cut -c21- >"$OUT/$name-inplace-log.txt"
"$here/dump-grants.sh" "$(db_of inplace)" >"$OUT/$name-grants-head-inplace.txt"
"$here/policy-probe.sh" http://127.0.0.1:4212 "$W/inplace/local-api-token" >"$OUT/$name-policy-probe.txt"
kill "$(cat "$W/inplace.pid")"; sleep 4
"$GHEAD" state -data-dir "$W/inplace" sync >/dev/null 2>&1
{ "$GHEAD" state check "$W/inplace/state-repo" 2>&1 && echo "state check: ok"; } | tail -3 >"$OUT/$name-state-check.txt"

echo "== dump route"
copy seed prepared
penv=()
[ "$kind" = postgres ] && penv=(GRAIN_DB_DRIVER=postgres "GRAIN_DB_DSN=$(db_of prepared)")
env "${penv[@]}" "$GHEAD" state -data-dir "$W/prepared" prepare-upgrade 2>&1 | grep -v 'deprecated admission' | tail -3 >"$OUT/$name-prepare-upgrade.txt"
if grep -q '^prepared' "$OUT/$name-prepare-upgrade.txt"; then
  "$here/start108.sh" "$GHEAD" "$W/prepared" 4213 $(store_flags prepared) || exit 1
  sleep 5
  "$here/dump-grants.sh" "$(db_of prepared)" >"$OUT/$name-grants-head-dump-route.txt"
  kill "$(cat "$W/prepared.pid")"
  diff "$OUT/$name-grants-head-inplace.txt" "$OUT/$name-grants-head-dump-route.txt" >"$OUT/$name-inplace-vs-dump-route.diff"
fi
diff "$OUT/$name-grants-108.txt" "$OUT/$name-grants-head-inplace.txt" >"$OUT/$name-108-vs-head-inplace.diff"
echo "done: $(ls "$OUT"/$name-* | wc -l) files"
