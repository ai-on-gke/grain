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

# item1_upgrade.sh <sqlite|postgres>: executes §3 item 1 end to end:
#   1. Seed schema-129 store with c08832a2 holding api:tasks:read@acme/widgets,
#      repo-read:acme/lib and bare self-debug across tasks, templates,
#      schedules, deployment/repo defaults, console sessions (with declined
#      api:ci-read default) and repo CI capabilities.
#   2. Rehearse `grain state prepare-upgrade` on a copy, and upgrade in-place
#      to HEAD (129 -> 131).
#   3. Verify console session declined default stays off, run Playwright UI
#      checks & per-surface partial-edit GET diffs (item1_ui_check.mjs), and
#      verify CapabilityPolicy denials on api#* at real task admission when
#      HasCapabilityRef == false.
set -euo pipefail

KIND="${1:-sqlite}"
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
OLD_BIN="${OLD_BIN:-/tmp/old/grain-c08832a2}"
HEAD_BIN="${HEAD_BIN:-/home/debian/bin/grain}"
OUT_DIR="${OUT_DIR:-$HERE/out}"
WORK="/var/tmp/unified-grants/item1-$KIND"
GHSIM_ADDR="127.0.0.1:4280"
OLD_PORT=4281
HEAD_PORT=4282

mkdir -p "$OUT_DIR"
chmod +x "$HERE/fake-agy.sh"

cleanup() {
  for pf in "$WORK/daemon.pid" "$WORK/ghsim.pid"; do
    if [[ -f "$pf" ]]; then
      pid=$(cat "$pf")
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 30); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
    fi
  done
}
trap cleanup EXIT

sudo pg_ctlcluster 15 main start >/dev/null 2>&1 || true

rm -rf "$WORK"
mkdir -p "$WORK/bin" "$WORK/repos" "$WORK/seed" "$WORK/scratch" "$WORK/home" "$WORK/walk"

(cd "$ROOT" && go build -o "$WORK/bin/ghsim" "$HERE/ghsim.go")

init_bare_repo() {
  local owner=$1 name=$2 def=$3 extra_branch=${4:-}
  local bare="$WORK/repos/$owner/$name.git"
  mkdir -p "$(dirname "$bare")"
  git init -q --bare -b "$def" "$bare"
  local tmp
  tmp=$(mktemp -d)
  git -C "$tmp" init -q -b "$def"
  echo "# $owner/$name" >"$tmp/README.md"
  git -C "$tmp" add README.md
  git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed $owner/$name"
  git -C "$tmp" push -q "$bare" "$def"
  if [[ -n "$extra_branch" ]]; then
    git -C "$tmp" checkout -q -b "$extra_branch"
    echo "branch $extra_branch" >>"$tmp/README.md"
    git -C "$tmp" commit -qam "chore: $extra_branch"
    git -C "$tmp" push -q "$bare" "$extra_branch"
  fi
  rm -rf "$tmp"
}

init_bare_repo acme widgets main release/v1
init_bare_repo acme lib main
init_bare_repo acme other trunk staging

"$WORK/bin/ghsim" -addr "$GHSIM_ADDR" -root "$WORK/repos" \
  -repo acme/widgets=main -repo acme/lib=main -repo acme/other=trunk \
  >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"

export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0 WALK_DIR="$WORK/walk"
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN

pg_admin() { psql -X -q "postgres://debian:walk@127.0.0.1:5432/postgres?sslmode=disable" -c "$1"; }
PG_SEED="ug_item1_seed"
PG_INPLACE="ug_item1_inplace"
PG_PREP="ug_item1_prep"

store_flags() {
  local db=$1
  if [[ "$KIND" == "postgres" ]]; then
    echo "-postgres-url postgres://debian:walk@127.0.0.1:5432/${db}?sslmode=disable"
  fi
}

if [[ "$KIND" == "postgres" ]]; then
  for db in "$PG_SEED" "$PG_INPLACE" "$PG_PREP"; do
    pg_admin "DROP DATABASE IF EXISTS $db;"
  done
  pg_admin "CREATE DATABASE $PG_SEED;"
fi

start_daemon() {
  local bin=$1 dir=$2 port=$3 pgdb=$4 extra=$5
  mkdir -p "$dir"
  # shellcheck disable=SC2086
  "$bin" daemon -data-dir "$dir" -ui-addr "127.0.0.1:$port" -sandbox-port-base=0 \
    -ci=false -poll-interval 1s -as owner@example.com \
    -default-target-repo acme/widgets \
    -github-host "$GHSIM_ADDR" -github-insecure-http \
    $(store_flags "$pgdb") $extra >"$dir.log" 2>&1 &
  echo $! >"$WORK/daemon.pid"
  for _ in $(seq 200); do
    kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null || { echo "daemon exited early; see $dir.log" >&2; tail -n 40 "$dir.log" >&2; exit 1; }
    if [[ -s "$dir/local-api-token" ]] && curl -fs -o /dev/null -H "Authorization: Bearer $(tr -d '[:space:]' <"$dir/local-api-token")" "http://127.0.0.1:$port/api/config"; then
      return 0
    fi
    sleep 0.15
  done
  echo "daemon on $port failed to start; see $dir.log" >&2
  exit 1
}

stop_daemon() {
  if [[ -f "$WORK/daemon.pid" ]]; then
    local pid
    pid=$(cat "$WORK/daemon.pid")
    kill "$pid" 2>/dev/null || true
    for _ in $(seq 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
    rm -f "$WORK/daemon.pid"
  fi
}

echo "== [$KIND] Step 1: Start c08832a2 (schema 129) and create objects =="
start_daemon "$OLD_BIN" "$WORK/seed" "$OLD_PORT" "$PG_SEED" "-disable-native-sandboxes"
TOK=$(tr -d '[:space:]' <"$WORK/seed/local-api-token")
API="http://127.0.0.1:$OLD_PORT"

call() {
  local method=$1 path=$2 body=${3:-}
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOK")
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" --data "$body")
  curl "${args[@]}" "$API$path"
}

call PUT /api/github-tokens/default '{"value":"ghp_walkthrough_stub"}' >/dev/null
for r in acme/widgets acme/lib acme/other; do
  call POST /api/repos "{\"repo\":\"$r\"}" >/dev/null
done
call PUT /api/config "{\"agentBackends\":[{\"id\":\"ag-walk\",\"name\":\"Walk Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$HERE/fake-agy.sh\",\"models\":[\"fake-model\"]}]}" >/dev/null
call PUT /api/agent-backends/ag-walk/credential '{"value":"walk-fake-gemini-key"}' >/dev/null

T1=$(call POST /api/tasks '{"title":"Mixed legacy task","description":"§3 item 1","repo":"acme/widgets","approved":false,"reads":["acme/lib"]}' | jq -r .id)
T2=$(call POST /api/tasks '{"title":"Policy legacy api task","description":"§3 item 1 admission","repo":"acme/widgets","approved":false}' | jq -r .id)
T3=$(call POST /api/tasks '{"title":"Policy canonical api task","description":"§3 item 1 admission","repo":"acme/widgets","approved":false}' | jq -r .id)
TPL=$(call POST /api/templates '{"name":"mixed-template","title":"Mixed template","description":"§3 item 1","repo":"acme/widgets","reads":["acme/lib"]}' | jq -r .id)
SCH=$(call POST /api/schedules '{"name":"mixed-schedule","title":"Mixed schedule","description":"§3 item 1","repo":"acme/widgets","reads":["acme/lib"],"recurrence":{"kind":"daily","timeOfDay":"03:00"}}' | jq -r .id)

call POST /api/console-sessions '{"id":"ops","title":"Ops","reads":["acme/lib"],"readsAll":false}' >/dev/null
# Wait for c08832a2's reconciler to create the ops and director session tasks & console_session_sync rows.
OPS_TASK=""
for _ in $(seq 40); do
  OPS_TASK=$(call GET /api/console-sessions | jq -r '.sessions[] | select(.id=="ops") | .taskId // empty')
  [[ -n "$OPS_TASK" ]] && break
  sleep 0.25
done
[[ -n "$OPS_TASK" ]] || { echo "ops session task was not created by c08832a2" >&2; exit 1; }

# Decline default api:ci-read on ops through c08832a2's own API!
call PUT /api/console-sessions/ops '{"capabilities":["api:tasks:read@acme/widgets","api:test-quarantine"]}' >/dev/null
sleep 1.5
stop_daemon

echo "== [$KIND] Step 2: Seed legacy api:, repo-read: and bare self-debug rows at schema 129 =="
q_seed() {
  if [[ "$KIND" == "postgres" ]]; then
    psql -X -q -At "postgres://debian:walk@127.0.0.1:5432/${PG_SEED}?sslmode=disable" -c "$1"
  else
    sqlite3 "$WORK/seed/store/grain.db" "$1"
  fi
}
exec_sql_seed() {
  if [[ "$KIND" == "postgres" ]]; then
    psql -X -q -v ON_ERROR_STOP=1 "postgres://debian:walk@127.0.0.1:5432/${PG_SEED}?sslmode=disable" -f "$1"
  else
    sqlite3 -bail "$WORK/seed/store/grain.db" <"$1"
  fi
}

SCHEMA_BEFORE=$(q_seed "SELECT version FROM grain_schema;")
echo "Schema before upgrade: $SCHEMA_BEFORE"
[[ "$SCHEMA_BEFORE" == "129" ]] || { echo "expected schema 129, got $SCHEMA_BEFORE" >&2; exit 1; }

q_seed "SELECT console_sessions FROM grain_config WHERE id=1" >"$WORK/cs.json"
q_seed "SELECT snapshot FROM console_session_sync WHERE task_id='$OPS_TASK'" >"$WORK/ops_sync.json"

SQL="$WORK/seed_legacy.sql"
python3 - "$SQL" "$T1" "$T2" "$T3" "$TPL" "$SCH" "$OPS_TASK" "$WORK/cs.json" "$WORK/ops_sync.json" <<'PY'
import json, sys

sql_path, t1, t2, t3, tpl, sch, ops_task, cs_path, sync_path = sys.argv[1:]
cs = json.loads(open(cs_path).read().strip() or "[]")
sync_raw = open(sync_path).read().strip()
sync = json.loads(sync_raw) if sync_raw else {}

mixed = ["api:tasks:read@acme/widgets", "self-debug", "api:test-quarantine"]

for s in cs:
    sid = s.get("id") or s.get("ID")
    if sid == "ops":
        s["capabilities"] = mixed
        s["declinedDefaults"] = ["api:ci-read"]

if sync:
    sync["capabilities"] = mixed
    sync["offeredDefaults"] = ["api:ci-read"]

def q(s):
    return "'" + s.replace("'", "''") + "'"

lines = [
    f"DELETE FROM task_grant WHERE task_id IN ({q(t1)}, {q(t2)}, {q(t3)}, {q(ops_task)});",
    f"DELETE FROM template_grant WHERE template_id = {q(tpl)};",
    f"DELETE FROM schedule_grant WHERE schedule_id = {q(sch)};",
]
for g in mixed:
    lines.append(f"INSERT INTO task_grant (task_id, capability, via) VALUES ({q(t1)}, {q(g)}, 'label');")
    lines.append(f"INSERT INTO template_grant (template_id, capability, via) VALUES ({q(tpl)}, {q(g)}, 'label');")
    lines.append(f"INSERT INTO schedule_grant (schedule_id, capability, via) VALUES ({q(sch)}, {q(g)}, 'label');")
    lines.append(f"INSERT INTO task_grant (task_id, capability, via) VALUES ({q(ops_task)}, {q(g)}, 'grain');")

canon_api = 'https://grain.dev/capabilities/api#tasks:read={"repo":"acme/widgets"}'
lines.append(f"INSERT INTO task_grant (task_id, capability, via) VALUES ({q(t2)}, 'api:tasks:read@acme/widgets', 'label');")
lines.append(f"INSERT INTO task_grant (task_id, capability, via) VALUES ({q(t3)}, {q(canon_api)}, 'label');")

lines.append(f"UPDATE grain_config SET default_capabilities = {q(json.dumps(mixed))}, console_sessions = {q(json.dumps(cs))} WHERE id = 1;")
if sync:
    lines.append(f"UPDATE console_session_sync SET snapshot = {q(json.dumps(sync))} WHERE task_id = {q(ops_task)};")

ci_caps_widgets = ["repo-read:acme/lib", "https://grain.dev/capabilities/github#sandbox-repo={}"]
ci_caps_lib = ["repo-read:acme/widgets", "self-debug"]
lines.append(
    "INSERT INTO repo_config (owner, name, default_capabilities, ci_capabilities) "
    f"VALUES ('acme', 'widgets', {q(json.dumps(mixed))}, {q(json.dumps(ci_caps_widgets))}) "
    "ON CONFLICT (owner, name) DO UPDATE SET default_capabilities = excluded.default_capabilities, ci_capabilities = excluded.ci_capabilities;"
)
lines.append(
    "INSERT INTO repo_config (owner, name, default_capabilities, ci_capabilities) "
    f"VALUES ('acme', 'lib', '[]', {q(json.dumps(ci_caps_lib))}) "
    "ON CONFLICT (owner, name) DO UPDATE SET ci_capabilities = excluded.ci_capabilities;"
)
open(sql_path, "w").write("\n".join(lines) + "\n")
PY
exec_sql_seed "$SQL"

jq -n --arg task1 "$T1" --arg task2 "$T2" --arg task3 "$T3" \
  --arg template "$TPL" --arg schedule "$SCH" --arg opsTask "$OPS_TASK" \
  '{task1:$task1, task2:$task2, task3:$task3, template:$template, schedule:$schedule, opsTask:$opsTask}' \
  >"$OUT_DIR/item1-ids-$KIND.json"

echo "== [$KIND] Step 3: Rehearse prepare-upgrade on a copy (129 -> 131) =="
cp -a "$WORK/seed" "$WORK/prep"
rm -f "$WORK/prep/db-config.json"
if [[ "$KIND" == "postgres" ]]; then
  pg_admin "CREATE DATABASE $PG_PREP TEMPLATE $PG_SEED;"
  GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN="postgres://debian:walk@127.0.0.1:5432/${PG_PREP}?sslmode=disable" \
    "$HEAD_BIN" state -data-dir "$WORK/prep" prepare-upgrade >"$OUT_DIR/item1-prepare-upgrade-$KIND.txt" 2>&1
else
  "$HEAD_BIN" state -data-dir "$WORK/prep" prepare-upgrade >"$OUT_DIR/item1-prepare-upgrade-$KIND.txt" 2>&1
fi
tail -n 5 "$OUT_DIR/item1-prepare-upgrade-$KIND.txt"

echo "== [$KIND] Step 4: Upgrade in-place to HEAD (129 -> 131) and run UI & GET diff checks =="
cp -a "$WORK/seed" "$WORK/inplace"
rm -f "$WORK/inplace/db-config.json"
if [[ "$KIND" == "postgres" ]]; then
  pg_admin "CREATE DATABASE $PG_INPLACE TEMPLATE $PG_SEED;"
fi

start_daemon "$HEAD_BIN" "$WORK/inplace" "$HEAD_PORT" "$PG_INPLACE" \
  "-host-sandboxes -sandbox-dir $WORK/scratch -sandbox-disk-floor-share 0 -sandbox-disk-floor-min-mb 0"
sleep 3

HTOK=$(tr -d '[:space:]' <"$WORK/inplace/local-api-token")
HAPI="http://127.0.0.1:$HEAD_PORT"
hcall() {
  local method=$1 path=$2 body=${3:-}
  local args=(-sS -X "$method" -H "Authorization: Bearer $HTOK")
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" --data "$body")
  curl "${args[@]}" "$HAPI$path"
}

if [[ "$KIND" == "postgres" ]]; then
  SCHEMA_AFTER=$(psql -X -q -At "postgres://debian:walk@127.0.0.1:5432/${PG_INPLACE}?sslmode=disable" -c "SELECT version FROM grain_schema;")
else
  SCHEMA_AFTER=$(sqlite3 "$WORK/inplace/store/grain.db" "SELECT version FROM grain_schema;")
fi
echo "Schema after upgrade: $SCHEMA_AFTER"

(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  node "$HERE/item1_ui_check.mjs" "$HAPI" "$HTOK" "$OUT_DIR/item1-ids-$KIND.json" "$OUT_DIR/item1-ui-$KIND.json")

echo "== [$KIND] Step 5: CapabilityPolicy denial on api#* at admission (HasCapabilityRef == false) =="
hcall PUT /api/capability-policy '{"policy":{"rules":[{"capability":"https://grain.dev/capabilities/api#tasks:read","deny":true}]}}' >"$OUT_DIR/item1-policy-put-$KIND.json"
hcall POST "/api/tasks/$T2/approve" '{}' >/dev/null
hcall POST "/api/tasks/$T3/approve" '{}' >/dev/null

# Wait up to 15s for orchestrator admission to judge both tasks.
for _ in $(seq 30); do
  s2=$(hcall GET "/api/tasks/$T2" | jq -r .state)
  s3=$(hcall GET "/api/tasks/$T3" | jq -r .state)
  if [[ "$s2" != "queued" && "$s3" != "queued" ]]; then
    break
  fi
  sleep 0.5
done

jq -n \
  --argjson t2 "$(hcall GET "/api/tasks/$T2" | jq '{id, state, capabilities, comments: [.comments[]? | {author: .author, body}]}')" \
  --argjson t3 "$(hcall GET "/api/tasks/$T3" | jq '{id, state, capabilities, comments: [.comments[]? | {author: .author, body}]}')" \
  '{legacyTask: $t2, canonicalTask: $t3}' >"$OUT_DIR/item1-admission-$KIND.json"

stop_daemon

echo "== [$KIND] Step 6: grain state sync + check on upgraded store =="
if [[ "$KIND" == "postgres" ]]; then
  GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN="postgres://debian:walk@127.0.0.1:5432/${PG_INPLACE}?sslmode=disable" \
    "$HEAD_BIN" state -data-dir "$WORK/inplace" sync >"$OUT_DIR/item1-state-sync-$KIND.txt" 2>&1
else
  "$HEAD_BIN" state -data-dir "$WORK/inplace" sync >"$OUT_DIR/item1-state-sync-$KIND.txt" 2>&1
fi
"$HEAD_BIN" state check "$WORK/inplace/state-repo" >"$OUT_DIR/item1-state-check-$KIND.txt" 2>&1
echo "[$KIND] DONE."
