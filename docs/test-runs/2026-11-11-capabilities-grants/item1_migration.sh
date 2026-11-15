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

# item1_migration.sh <sqlite|postgres>: §6 item 1 of
# docs/test-plans/2026-11-11.md. A real 61668e58 daemon (schema 131) creates a
# task, a template, a schedule, a console session and repo configs; the store
# is then seeded with every legacy grant spelling (api:<what>[@repo], a
# non-canonical api#<what>={} reference, repo-read:, repo-write:) in every
# table migration 132 rewrites, including spellings that collapse into one
# canonical row. HEAD then opens the store in place; the script dumps every
# grant column and fails if any legacy spelling or duplicate survives, or if
# a grant was lost. Finally a hand-edited old grain/config dump is imported.
set -euo pipefail

KIND="${1:-sqlite}"
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
OLD_BIN="${OLD_BIN:-/home/debian/bin/grain-old}"
HEAD_BIN="${HEAD_BIN:-/home/debian/bin/grain}"
OUT="${OUT_DIR:-$HERE/out}"
WORK="/var/tmp/capgrants/item1-$KIND"
GHSIM_ADDR="127.0.0.1:4290"
PORT=4291
PGURL="postgres://debian:walk@127.0.0.1:5432"
PGDB="cg_item1"
mkdir -p "$OUT"

cleanup() { for pf in "$WORK"/*.pid; do [[ -f "$pf" ]] && kill "$(cat "$pf")" 2>/dev/null || true; done; }
trap cleanup EXIT
rm -rf "$WORK"; mkdir -p "$WORK/repos" "$WORK/data" "$WORK/home"
(cd "$ROOT" && go build -o "$WORK/ghsim" ./docs/test-runs/2026-11-10-unified-grants/ghsim.go)
export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN
for r in widgets lib other; do
  git init -q --bare -b main "$WORK/repos/acme/$r.git"
  t=$(mktemp -d); git -C "$t" init -q -b main; echo "# acme/$r" >"$t/README.md"
  git -C "$t" add . && git -C "$t" -c user.name=s -c user.email=s@example.com commit -qm seed
  git -C "$t" push -q "$WORK/repos/acme/$r.git" main; rm -rf "$t"
done
"$WORK/ghsim" -addr "$GHSIM_ADDR" -root "$WORK/repos" -repo acme/widgets=main -repo acme/lib=main -repo acme/other=main >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"

store() { [[ "$KIND" == postgres ]] && echo "-postgres-url $PGURL/$PGDB?sslmode=disable" || true; }
q() {
  if [[ "$KIND" == postgres ]]; then psql -X -q -At -v ON_ERROR_STOP=1 "$PGURL/$PGDB?sslmode=disable" -c "$1"
  else sqlite3 -bail "$WORK/data/store/grain.db" "$1"; fi
}
if [[ "$KIND" == postgres ]]; then
  psql -X -q "$PGURL/postgres?sslmode=disable" -c "DROP DATABASE IF EXISTS $PGDB" -c "CREATE DATABASE $PGDB"
fi

start() {
  # shellcheck disable=SC2046
  "$1" daemon -data-dir "$WORK/data" -ui-addr "127.0.0.1:$PORT" -sandbox-port-base=0 -ci=false \
    -poll-interval 1s -as owner@example.com -default-target-repo acme/widgets -disable-native-sandboxes \
    -github-host "$GHSIM_ADDR" -github-insecure-http $(store) >>"$WORK/daemon.log" 2>&1 &
  echo $! >"$WORK/daemon.pid"
  for _ in $(seq 300); do
    kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null || { tail -n 30 "$WORK/daemon.log"; exit 1; }
    if [[ -s "$WORK/data/local-api-token" ]] && curl -fs -o /dev/null -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")" "http://127.0.0.1:$PORT/api/config"; then return; fi
    sleep 0.2
  done
  echo "daemon did not start"; exit 1
}
stop() { kill "$(cat "$WORK/daemon.pid")"; while kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null; do sleep 0.1; done; rm -f "$WORK/daemon.pid"; }
call() {
  local a=(-sS -X "$1" -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")")
  [[ -n "${3:-}" ]] && a+=(-H "Content-Type: application/json" --data "$3")
  curl "${a[@]}" "http://127.0.0.1:$PORT$2"
}

echo "== [$KIND] 61668e58: create objects at schema 131"
start "$OLD_BIN"
call PUT /api/github-tokens/default '{"value":"ghp_walkthrough_stub"}' >/dev/null
for r in acme/widgets acme/lib acme/other; do call POST /api/repos "{\"repo\":\"$r\"}" >/dev/null; done
T1=$(call POST /api/tasks '{"title":"Held legacy task","description":"item 1","repo":"acme/widgets","approved":false}' | jq -r .id)
T2=$(call POST /api/tasks '{"title":"Grant-wait task","description":"item 1","repo":"acme/widgets","approved":false}' | jq -r .id)
TPL=$(call POST /api/templates '{"name":"legacy-template","title":"Legacy template","description":"item 1","repo":"acme/widgets"}' | jq -r .id)
SCH=$(call POST /api/schedules '{"name":"legacy-schedule","title":"Legacy schedule","description":"item 1","repo":"acme/widgets","recurrence":{"kind":"daily","timeOfDay":"03:00"}}' | jq -r .id)
call POST /api/console-sessions '{"id":"ops","title":"Ops","readsAll":false}' >/dev/null
sleep 2
stop
echo "schema before: $(q 'SELECT version FROM grain_schema')"

CANON_TR='https://grain.dev/capabilities/api#tasks:read={"repo":"acme/widgets"}'
python3 - "$WORK/seed.sql" "$T1" "$T2" "$TPL" "$SCH" "$(q 'SELECT console_sessions FROM grain_config WHERE id=1')" "$CANON_TR" <<'PY'
import json, sys
path, t1, t2, tpl, sch, cs, canon_tr = sys.argv[1:]
legacy = [
    "api:tasks:read@acme/widgets",          # legacy, repo-scoped
    canon_tr,                               # canonical twin: must collapse to one row
    "api:ci-read",                          # legacy, unscoped
    "https://grain.dev/capabilities/api#test-quarantine={}",  # non-canonical ={} form
    "repo-read:acme/lib",
    "repo-write:acme/other",
    "release-gate-read",                    # bare in-process name: left alone
]
def s(v): return "'" + v.replace("'", "''") + "'"
out = []
for table, col, oid in (("task_grant", "task_id", t1), ("template_grant", "template_id", tpl), ("schedule_grant", "schedule_id", sch)):
    out.append(f"DELETE FROM {table} WHERE {col}={s(oid)};")
    for g in legacy:
        out.append(f"INSERT INTO {table} ({col}, capability, via) VALUES ({s(oid)}, {s(g)}, 'label');")
out.append(f"INSERT INTO task_grant_wait (task_id, capability, resource, waiting_at) VALUES ({s(t2)}, 'api:ci-read@acme/widgets', '', CURRENT_TIMESTAMP);")
sessions = json.loads(cs or "[]")
for x in sessions:
    if x.get("ID") == "ops":
        x["Capabilities"] = ["api:tasks:read@acme/widgets", canon_tr, "repo-read:acme/lib"]
        x["declinedDefaults"] = ["api:ci-read@acme/widgets"]
policy = {"catalogue": {"defaults": {"task": ["api:tasks:read"], "ci": ["repo-read:acme/lib"],
          "repos": {"acme/widgets": {"task": ["api:ci-read@acme/widgets"], "ci": ["repo-write:acme/other"]}}}}}
out.append(f"UPDATE grain_config SET default_capabilities={s(json.dumps(legacy))}, console_sessions={s(json.dumps(sessions))}, capability_policy={s(json.dumps(policy))} WHERE id=1;")
ci = json.dumps(["repo-read:acme/lib", 'https://grain.dev/capabilities/repo#read={"repo":"acme/lib"}', "repo-write:acme/other"])
out.append("INSERT INTO repo_config (owner, name, default_capabilities, ci_capabilities) "
           f"VALUES ('acme', 'widgets', {s(json.dumps(legacy))}, {s(ci)}) ON CONFLICT (owner, name) DO UPDATE SET "
           "default_capabilities = excluded.default_capabilities, ci_capabilities = excluded.ci_capabilities;")
open(path, "w").write("\n".join(out) + "\n")
PY
if [[ "$KIND" == postgres ]]; then psql -X -q -v ON_ERROR_STOP=1 "$PGURL/$PGDB?sslmode=disable" -f "$WORK/seed.sql"; else sqlite3 -bail "$WORK/data/store/grain.db" <"$WORK/seed.sql"; fi

dump() {
  {
    echo "schema=$(q 'SELECT version FROM grain_schema')"
    echo "-- task_grant $T1"; q "SELECT capability FROM task_grant WHERE task_id='$T1' ORDER BY capability"
    echo "-- template_grant"; q "SELECT capability FROM template_grant WHERE template_id='$TPL' ORDER BY capability"
    echo "-- schedule_grant"; q "SELECT capability FROM schedule_grant WHERE schedule_id='$SCH' ORDER BY capability"
    echo "-- task_grant_wait"; q "SELECT capability FROM task_grant_wait WHERE task_id='$T2'"
    echo "-- grain_config.default_capabilities"; q "SELECT default_capabilities FROM grain_config WHERE id=1"
    echo "-- repo_config acme/widgets default/ci"; q "SELECT default_capabilities FROM repo_config WHERE owner='acme' AND name='widgets'"
    q "SELECT ci_capabilities FROM repo_config WHERE owner='acme' AND name='widgets'"
    echo "-- console session ops"; q "SELECT console_sessions FROM grain_config WHERE id=1" | jq -c '.[] | select(.ID=="ops") | {Capabilities, declinedDefaults}'
    echo "-- capability_policy catalogue defaults"; q "SELECT capability_policy FROM grain_config WHERE id=1" | jq -c '.catalogue.defaults'
  }
}
dump >"$OUT/item1-$KIND-before.txt"

echo "== [$KIND] HEAD: open in place (131 -> current)"
start "$HEAD_BIN"
sleep 2
API_T1=$(call GET "/api/tasks/$T1" | jq -c '.capabilities')
stop
dump >"$OUT/item1-$KIND-after.txt"
echo "task $T1 capabilities via API: $API_T1" >>"$OUT/item1-$KIND-after.txt"

fail=0
if grep -E '(^|"|\[)(api:|repo-read:|repo-write:)' "$OUT/item1-$KIND-after.txt" | grep -v '^task '; then echo "FAIL: legacy spelling survived"; fail=1; fi
if grep -F '={}' "$OUT/item1-$KIND-after.txt"; then echo "FAIL: non-canonical ={} survived"; fail=1; fi
for tbl in task_grant template_grant schedule_grant; do
  n=$(awk -v t="-- $tbl" '$0 ~ "^-- " {on = ($1" "$2 == t); next} on' "$OUT/item1-$KIND-after.txt" | sort | uniq | wc -l)
  d=$(awk -v t="-- $tbl" '$0 ~ "^-- " {on = ($1" "$2 == t); next} on' "$OUT/item1-$KIND-after.txt" | wc -l)
  echo "$tbl: $d rows, $n distinct (want 6 = 7 seeded minus one canonical twin)"
  [[ "$n" == 6 && "$d" == 6 ]] || fail=1
done

echo "== [$KIND] import a hand-edited grain/config"
# Export HEAD's store, then put legacy spellings back into settings/templates.yml
# and settings/schedules.yml as a person editing an old grain/config by hand
# would, commit, and let the running daemon apply the change.
if [[ "$KIND" == postgres ]]; then
  GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN="$PGURL/$PGDB?sslmode=disable" "$HEAD_BIN" state -data-dir "$WORK/data" sync >"$WORK/sync.log" 2>&1
else
  "$HEAD_BIN" state -data-dir "$WORK/data" sync >"$WORK/sync.log" 2>&1
fi
SR="$WORK/data/state-repo"
start "$HEAD_BIN"
python3 - "$SR/settings/templates.yml" "$SR/settings/schedules.yml" <<'PY'
import re, sys
for p in sys.argv[1:]:
    s = open(p).read()
    s = s.replace('- https://grain.dev/capabilities/api#tasks:read={"repo":"acme/widgets"}', '- api:tasks:read@acme/widgets\n      - https://grain.dev/capabilities/api#tasks:read={"repo":"acme/widgets"}')
    s = s.replace('- https://grain.dev/capabilities/repo#read={"repo":"acme/lib"}', '- repo-read:acme/lib')
    s = s.replace('- https://grain.dev/capabilities/api#test-quarantine\n', '- api:test-quarantine\n')
    s = s.replace('- https://grain.dev/capabilities/api#ci-read\n', '- api:ci-read\n      - api:releases:read@acme/widgets\n')
    open(p, "w").write(s)
PY
git -C "$SR" -c user.name=hand -c user.email=hand@example.com commit -qam "hand edit: legacy grant spellings"
for _ in $(seq 60); do
  n=$(q "SELECT count(*) FROM template_grant WHERE template_id='$TPL'")
  [[ "$n" == 7 ]] && break
  sleep 0.5
done
{
  echo "-- template_grant after import"; q "SELECT capability FROM template_grant WHERE template_id='$TPL' ORDER BY capability"
  echo "-- schedule_grant after import"; q "SELECT capability FROM schedule_grant WHERE schedule_id='$SCH' ORDER BY capability"
} >"$OUT/item1-$KIND-import.txt"
stop
cat "$OUT/item1-$KIND-import.txt"
if grep -E '^(api:|repo-read:|repo-write:)' "$OUT/item1-$KIND-import.txt"; then echo "FAIL: import kept a legacy spelling"; fail=1; fi
grep -q 'api#releases:read={"repo":"acme/widgets"}' "$OUT/item1-$KIND-import.txt" || { echo "FAIL: added grant lost on import"; fail=1; }
echo "[$KIND] fail=$fail"
exit $fail
