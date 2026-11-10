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

# walk_item_4_upgrade.sh exercises §4 item 4:
#   - schema 129 -> 131 (and -> HEAD) and schema 130 -> 131 (and -> HEAD)
#     in-place upgrades and `grain state prepare-upgrade` on SQLite and PostgreSQL
#   - `grain_ci` defaulting to 0/false on existing schedules
#   - `grain state sync`, `grain state check`, and state dump/import round-trips
#     carrying `grain_ci` (including `grain-ci: true` in settings/schedules.yml).
set -euo pipefail

G129=${G129:-/home/debian/bin/grain-129}
G130=${G130:-/home/debian/bin/grain-130}
G131=${G131:-/home/debian/bin/grain-131}
GHEAD=${GHEAD:-/home/debian/bin/grain}
ROOT=${ROOT:-/tmp/walk-item4}
PG_ADMIN="postgres://debian:walk@127.0.0.1:5432/postgres?sslmode=disable"

cleanup_pids() {
  for p in "$ROOT"/*.pid; do
    [ -f "$p" ] && kill "$(cat "$p")" 2>/dev/null || true
  done
}
trap cleanup_pids EXIT

rm -rf "$ROOT"
mkdir -p "$ROOT/out"

pg_admin() { psql -X -q "$PG_ADMIN" -c "$1"; }
pg_url() { echo "postgres://debian:walk@127.0.0.1:5432/$1?sslmode=disable"; }

start_daemon() {
  local bin=$1 dir=$2 port=$3; shift 3
  mkdir -p "$dir"
  setsid "$bin" daemon \
    -data-dir "$dir" \
    -ui-addr "127.0.0.1:$port" \
    -disable-native-sandboxes \
    -sandbox-port-base=0 \
    "$@" >"$dir.log" 2>&1 </dev/null &
  echo $! >"$dir.pid"
  for _ in $(seq 45); do
    kill -0 "$(cat "$dir.pid")" 2>/dev/null || { echo "daemon on $port exited:"; tail -n 30 "$dir.log"; return 1; }
    if [ -s "$dir/local-api-token" ] && curl -fs -o /dev/null -H "Authorization: Bearer $(cat "$dir/local-api-token")" "http://127.0.0.1:$port/api/config"; then
      return 0
    fi
    sleep 0.4
  done
  echo "daemon on $port timed out:"; tail -n 30 "$dir.log"; return 1
}

stop_daemon() {
  local dir=$1
  [ -f "$dir.pid" ] || return 0
  local pid
  pid=$(cat "$dir.pid")
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 30); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.2
  done
  rm -f "$dir.pid"
}

api_call() {
  local dir=$1 port=$2 method=$3 path=$4 body=${5:-}
  local tok
  tok=$(cat "$dir/local-api-token")
  if [ -n "$body" ]; then
    curl -fsS -X "$method" -H "Authorization: Bearer $tok" -H "Content-Type: application/json" --data "$body" "http://127.0.0.1:$port$path"
  else
    curl -fsS -X "$method" -H "Authorization: Bearer $tok" "http://127.0.0.1:$port$path"
  fi
}

query_schedules() {
  local kind=$1 target=$2
  if [ "$kind" = "postgres" ]; then
    psql -X -q -At "$target" -c "SELECT id, COALESCE(template_id,''), grain_ci FROM schedule WHERE id NOT LIKE 'system:%' ORDER BY id;"
  else
    sqlite3 "$target" "SELECT id || '|' || COALESCE(template_id,'') || '|' || grain_ci FROM schedule WHERE id NOT LIKE 'system:%' ORDER BY id;"
  fi
}

run_matrix_case() {
  local from_ver=$1 old_bin=$2 to_label=$3 new_bin=$4 kind=$5 port_base=$6
  local tag="s${from_ver}_to_${to_label}_${kind}"
  echo "=== Case $tag ==="
  local d_seed="$ROOT/${tag}_seed"
  local d_inpl="$ROOT/${tag}_inplace"
  local d_prep="$ROOT/${tag}_prepared"

  local flags_seed=() flags_inpl=() flags_prep=()
  local db_seed="$d_seed/store/grain.db"
  local db_inpl="$d_inpl/store/grain.db"
  local db_prep="$d_prep/store/grain.db"

  if [ "$kind" = "postgres" ]; then
    for sub in seed inpl prep; do
      pg_admin "DROP DATABASE IF EXISTS ${tag}_${sub};"
    done
    pg_admin "CREATE DATABASE ${tag}_seed;"
    db_seed=$(pg_url "${tag}_seed")
    db_inpl=$(pg_url "${tag}_inpl")
    db_prep=$(pg_url "${tag}_prep")
    flags_seed=(-postgres-url "$db_seed")
    flags_inpl=(-postgres-url "$db_inpl")
    flags_prep=(-postgres-url "$db_prep")
  fi

  start_daemon "$old_bin" "$d_seed" "$port_base" "${flags_seed[@]}"
  api_call "$d_seed" "$port_base" PUT /api/github-tokens/default '{"value":"walk-stub-token"}' >/dev/null
  api_call "$d_seed" "$port_base" POST /api/repos '{"repo":"acme/widgets"}' >/dev/null
  TPL_ID=$(api_call "$d_seed" "$port_base" POST /api/templates '{"name":"tmpl-check","title":"Template check","description":"body","repo":"acme/widgets"}' | jq -r '.id')
  S1_ID=$(api_call "$d_seed" "$port_base" POST /api/schedules '{"title":"Inline chore","description":"chore","repo":"acme/widgets","recurrence":{"kind":"daily","timeOfDay":"03:00"}}' | jq -r '.id')
  S2_ID=$(api_call "$d_seed" "$port_base" POST /api/schedules "{\"repo\":\"acme/widgets\",\"templateId\":\"$TPL_ID\",\"recurrence\":{\"kind\":\"everyNHours\",\"everyNHours\":6}}" | jq -r '.id')
  stop_daemon "$d_seed"

  # Also seed a workflow-review settings schedule row directly as declared/settings workflow row.
  if [ "$kind" = "postgres" ]; then
    psql -X -q "$db_seed" -c "INSERT INTO schedule (id, title, body, target_owner, target_name, base, auto_merge, auto_approve_proposals, template_id, recurrence_kind, every_n_hours, overlap, enabled, next_run_at, created_at, workflow_review) VALUES ('sched-wf', 'Workflow check', '', 'acme', 'widgets', 'main', false, true, 'system:workflow-review:check', 'every_n_hours', 6, 'skip', true, NOW(), NOW(), 'on-failure');"
  else
    sqlite3 "$db_seed" "INSERT INTO schedule (id, title, body, target_owner, target_name, base, auto_merge, auto_approve_proposals, template_id, recurrence_kind, every_n_hours, overlap, enabled, next_run_at, created_at, workflow_review) VALUES ('sched-wf', 'Workflow check', '', 'acme', 'widgets', 'main', 0, 1, 'system:workflow-review:check', 'every_n_hours', 6, 'skip', 1, '2026-11-10 12:00:00 +0000 UTC', '2026-11-10 06:00:00 +0000 UTC', 'on-failure');"
  fi

  # Copy seed -> inplace & prepared
  cp -a "$d_seed" "$d_inpl"
  cp -a "$d_seed" "$d_prep"
  rm -f "$d_inpl/db-config.json" "$d_prep/db-config.json"
  if [ "$kind" = "postgres" ]; then
    pg_admin "CREATE DATABASE ${tag}_inpl TEMPLATE ${tag}_seed;"
    pg_admin "CREATE DATABASE ${tag}_prep TEMPLATE ${tag}_seed;"
  fi

  # 1. In-place upgrade
  start_daemon "$new_bin" "$d_inpl" "$((port_base + 1))" "${flags_inpl[@]}"
  stop_daemon "$d_inpl"
  echo "[$tag] in-place schedules (id|template_id|grain_ci):"
  query_schedules "$kind" "$db_inpl" | tee "$ROOT/out/${tag}_inplace.txt"

  # 2. prepare-upgrade route
  local penv=()
  [ "$kind" = "postgres" ] && penv=(GRAIN_DB_DRIVER=postgres "GRAIN_DB_DSN=$db_prep")
  set +e
  env "${penv[@]}" "$new_bin" state -data-dir "$d_prep" prepare-upgrade >"$ROOT/out/${tag}_prepare.txt" 2>&1
  PREP_RC=$?
  set -e
  echo "[$tag] prepare-upgrade exit=$PREP_RC: $(tail -n 2 "$ROOT/out/${tag}_prepare.txt")"
  start_daemon "$new_bin" "$d_prep" "$((port_base + 2))" "${flags_prep[@]}"
  stop_daemon "$d_prep"
  echo "[$tag] prepare-upgrade schedules (id|template_id|grain_ci):"
  query_schedules "$kind" "$db_prep" | tee "$ROOT/out/${tag}_prepared_scheds.txt"

  # 3. Now set grain_ci = true/1 on sched-wf, run `grain state sync` + `grain state check`,
  #    and inspect how settings/schedules.yml writes `grain-ci:`!
  if [ "$kind" = "postgres" ]; then
    psql -X -q "$db_inpl" -c "UPDATE schedule SET grain_ci = true WHERE id = 'sched-wf';"
  else
    sqlite3 "$db_inpl" "UPDATE schedule SET grain_ci = 1 WHERE id = 'sched-wf';"
  fi
  set +e
  env "${penv[@]:+${penv[@]}}" $([ "$kind" = "postgres" ] && echo "GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN=$db_inpl") \
    "$new_bin" state -data-dir "$d_inpl" sync >"$ROOT/out/${tag}_sync.txt" 2>&1
  SYNC_RC=$?
  "$new_bin" state check "$d_inpl/state-repo" >"$ROOT/out/${tag}_check.txt" 2>&1
  CHECK_RC=$?
  set -e
  echo "[$tag] state sync exit=$SYNC_RC, state check exit=$CHECK_RC: $(tail -n 2 "$ROOT/out/${tag}_check.txt")"
  echo "[$tag] grain-ci lines in exported settings/schedules.yml:"
  grep -n -C 2 "grain-ci" "$d_inpl/state-repo/settings/schedules.yml" | tee "$ROOT/out/${tag}_exported_yaml.txt" || true

  # 4. Hand-edit `grain-ci: true` and `grain-ci: false` as YAML booleans in settings/schedules.yml
  #    (as docs/declarations.md & docs/schedules.md instruct) and run `grain state check` + import (`sync` in external/apply or fresh import)!
  cp -a "$d_inpl/state-repo" "$ROOT/${tag}_edited_repo"
  sed -i 's/grain-ci: 1/grain-ci: true/g; s/grain-ci: 0/grain-ci: false/g' "$ROOT/${tag}_edited_repo/settings/schedules.yml"
  # Also flip S1_ID or check round-trip: keep sched-wf true, others false.
  set +e
  "$new_bin" state check "$ROOT/${tag}_edited_repo" >"$ROOT/out/${tag}_bool_check.txt" 2>&1
  BOOL_CHECK_RC=$?
  set -e
  echo "[$tag] state check with YAML boolean grain-ci: true/false -> exit=$BOOL_CHECK_RC: $(cat "$ROOT/out/${tag}_bool_check.txt")"

  # Test importing that state-repo into a fresh store on the SAME backend!
  git -C "$ROOT/${tag}_edited_repo" config user.name "Alice Example"
  git -C "$ROOT/${tag}_edited_repo" config user.email "alice@example.com"
  git -C "$ROOT/${tag}_edited_repo" commit -q -am "Use YAML booleans for grain-ci" || true

  local d_imp="$ROOT/${tag}_imported"
  mkdir -p "$d_imp"
  local flags_imp=() db_imp="$d_imp/store/grain.db"
  if [ "$kind" = "postgres" ]; then
    pg_admin "DROP DATABASE IF EXISTS ${tag}_imp;"
    pg_admin "CREATE DATABASE ${tag}_imp;"
    db_imp=$(pg_url "${tag}_imp")
    flags_imp=(-postgres-url "$db_imp")
  fi
  "$new_bin" state -data-dir "$d_imp" repo "$ROOT/${tag}_edited_repo" >/dev/null
  set +e
  start_daemon "$new_bin" "$d_imp" "$((port_base + 3))" "${flags_imp[@]}"
  IMP_RC=$?
  set -e
  stop_daemon "$d_imp"
  echo "[$tag] fresh daemon import from state-repo exit=$IMP_RC"
  if [ "$IMP_RC" = "0" ]; then
    query_schedules "$kind" "$db_imp" | tee "$ROOT/out/${tag}_imported_scheds.txt"
  else
    tail -n 20 "$d_imp.log" | tee "$ROOT/out/${tag}_imported_err.txt"
  fi
}

run_matrix_case 129 "$G129" 131 "$G131" sqlite   4260
run_matrix_case 130 "$G130" 131 "$G131" sqlite   4264
run_matrix_case 129 "$G129" head "$GHEAD" sqlite 4268
run_matrix_case 129 "$G129" 131 "$G131" postgres 4272
run_matrix_case 130 "$G130" 131 "$G131" postgres 4276
run_matrix_case 129 "$G129" head "$GHEAD" postgres 4280

echo "=== Cross-backend check: import SQLite-exported state-repo (grain-ci: 0/1) into PostgreSQL ==="
pg_admin "DROP DATABASE IF EXISTS cross_sqlite_to_pg;"
pg_admin "CREATE DATABASE cross_sqlite_to_pg;"
mkdir -p "$ROOT/cross_pg"
"$GHEAD" state -data-dir "$ROOT/cross_pg" repo "$ROOT/s129_to_head_sqlite_inplace/state-repo" >/dev/null
set +e
start_daemon "$GHEAD" "$ROOT/cross_pg" 4285 -postgres-url "$(pg_url cross_sqlite_to_pg)"
CROSS_RC=$?
set -e
stop_daemon "$ROOT/cross_pg"
echo "SQLite state-repo -> PostgreSQL daemon import exit=$CROSS_RC"
if [ "$CROSS_RC" = "0" ]; then
  query_schedules postgres "$(pg_url cross_sqlite_to_pg)" | tee "$ROOT/out/cross_sqlite_to_pg.txt"
else
  tail -n 15 "$ROOT/cross_pg.log" | tee "$ROOT/out/cross_sqlite_to_pg_err.txt"
fi

echo "=== ALL ITEM 4 MATRIX CASES DONE ==="
