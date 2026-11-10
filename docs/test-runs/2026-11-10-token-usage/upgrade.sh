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

# upgrade.sh <sqlite|postgres>: §1 item 2. A schema-129 daemon (c08832a2,
# /home/debian/bin/grain-s129) runs a real task through fake-agy; then HEAD
# (schema 131) opens a copy in place, and `grain state prepare-upgrade`
# takes a second copy through the dump route. Each is checked with
# `grain state sync` + `grain state check`, the old run's usage is read
# back through the API (it must be absent, not zero) and a new run must
# record usage. Output goes to out/upgrade-<kind>-*.txt.
kind=$1
D=$(cd "$(dirname "$0")" && pwd); OUT=$D/out; W=/home/debian/walk/up-$kind
OLD=/home/debian/bin/grain-s129 HEAD=/home/debian/bin/grain
rm -rf "$W" && mkdir -p "$W/w/runs"
export WALK_DIR=$W/w
pgdsn() { echo "postgres://debian:walk@127.0.0.1:5432/$1?sslmode=disable"; }
flags() { [ "$kind" = postgres ] && echo "-postgres-url $(pgdsn "tu_up_$1")"; }
stop() { kill "$(cat "$W/$1.pid")"; sleep 3; }
api() { "$D/api.sh" "$W/$1" "$2" GET "$3" | head -n 1; }
if [ "$kind" = postgres ]; then
  for db in seed inplace prepared; do psql -h 127.0.0.1 -U debian -d postgres -qc "DROP DATABASE IF EXISTS tu_up_$db" </dev/null; done
  psql -h 127.0.0.1 -U debian -d postgres -qc "CREATE DATABASE tu_up_seed" </dev/null
fi
copy() { # copy <from> <to>
  cp -a "$W/$1" "$W/$2"
  [ "$kind" = postgres ] && psql -h 127.0.0.1 -U debian -d postgres -qc "CREATE DATABASE tu_up_$2 TEMPLATE tu_up_$1" </dev/null
}
echo "== seed on schema 129"
"$D/start.sh" "$OLD" "$W/seed" 4311 $(flags seed) || exit 1
"$D/setup.sh" "$W/seed" 4311 /home/debian/walk/fake-agy >/dev/null
id=$("$D/task.sh" "$W/seed" 4311 "$W/w" ok "pre-upgrade run")
api seed 4311 "/api/tasks/$id" | jq -c '{id, attempts: [.attempts[] | {number, outcome}]}' >"$OUT/upgrade-$kind-seed.txt"
stop seed

echo "== in place"
copy seed inplace
"$D/start.sh" "$HEAD" "$W/inplace" 4312 $(flags inplace) || exit 1
grep -E 'migrat|schema' "$W/inplace.log" | cut -c21- >"$OUT/upgrade-$kind-inplace-log.txt"
{
  echo "# the pre-upgrade task: task total and per-attempt usage (null = none recorded)"
  api inplace 4312 "/api/tasks/$id" | jq -c '{usage, attempts: [.attempts[] | {number, outcome, usage}]}'
  id2=$("$D/task.sh" "$W/inplace" 4312 "$W/w" ok "post-upgrade run")
  echo "# a post-upgrade task"
  api inplace 4312 "/api/tasks/$id2" | jq -c '{usage, attempts: [.attempts[] | {number, outcome, usage: (.usage | {inputTokens, outputTokens, cacheReadTokens, turns})}]}'
  echo "# the CLI's emitted report"; cat "$W/w/emitted.log"
} >"$OUT/upgrade-$kind-inplace-api.txt" 2>&1
stop inplace
"$HEAD" state -data-dir "$W/inplace" sync >"$W/sync.log" 2>&1
{ "$HEAD" state check "$W/inplace/state-repo" 2>&1 && echo "state check: ok"; } | tail -n 3 >"$OUT/upgrade-$kind-state-check.txt"
grep -l 'task_run_usage' -r "$W/inplace/state-repo" | head -n 3 | sed "s|$W/||" >>"$OUT/upgrade-$kind-state-check.txt"

echo "== dump route"
copy seed prepared
penv=()
[ "$kind" = postgres ] && penv=(GRAIN_DB_DRIVER=postgres "GRAIN_DB_DSN=$(pgdsn tu_up_prepared)")
env "${penv[@]}" "$HEAD" state -data-dir "$W/prepared" prepare-upgrade 2>&1 | tail -n 3 >"$OUT/upgrade-$kind-prepare-upgrade.txt"
"$D/start.sh" "$HEAD" "$W/prepared" 4313 $(flags prepared) || exit 1
grep -E 'migrat|import|schema' "$W/prepared.log" | cut -c21- >"$OUT/upgrade-$kind-prepared-log.txt"
api prepared 4313 "/api/tasks/$id" | jq -c '{usage, attempts: [.attempts[] | {number, outcome, usage}]}' >>"$OUT/upgrade-$kind-prepared-log.txt"
stop prepared
"$HEAD" state -data-dir "$W/prepared" sync >"$W/sync2.log" 2>&1
{ "$HEAD" state check "$W/prepared/state-repo" 2>&1 && echo "state check: ok"; } | tail -n 3 >>"$OUT/upgrade-$kind-prepare-upgrade.txt"
echo "done"; ls "$OUT"/upgrade-$kind-*
