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

# env.sh starts the §8 environment (docs/test-plans/2026-11-13.md) and
# leaves it running:
#   - githubsim_server (githubsim REST + git http-backend) on 127.0.0.1:4270
#     serving acme/widgets, acme/upstream and acme/downstream;
#   - a real `grain daemon -sandbox-port-base=0` on 127.0.0.1:4271 over
#     SQLite (DB=sqlite, the default) or PostgreSQL (DB=postgres, a local
#     cluster with a superuser grain/grain), host sandboxes, the simulator
#     as its GitHub host and a fake agent that exits at once.
#
# acme/widgets declares:
#   .grain/templates/plain.yml   a bare template
#   .grain/templates/big.yml     sandbox: {disk-gb: 60}
#   .grain/templates/nightly.yml admission: durable, repo#read acme/upstream, api#memory
#   .grain/schedules/held.yml    plain, skip-when: publishing-held
#   .grain/schedules/free.yml    plain, no skip-when
#   .grain/schedules/big.yml     big
#   .grain/schedules/own.yml     nightly + its own api#ci-read grant
# every 10 minutes (the floor); step scripts pull next_run_at forward.
#
# The pipeline is paused (PAUSE=0 leaves it running) so every filed task
# stays queued for inspection. KEEP=1 restarts only the daemon over the
# same data. LEGACY=1 leaves out the keys a schema-139 build refuses.
# Source helpers.sh afterwards for `api`, `db` and friends.
set -euo pipefail

GRAIN_BIN=${GRAIN_BIN:-/home/debian/bin/grain}
SIM_BIN=${SIM_BIN:-/home/debian/bin/githubsim_server}
DB=${DB:-sqlite}
WORK_ROOT=${WORK_ROOT:-/var/tmp/walk-sched-tmpl-$DB}
SIM_PORT=${SIM_PORT:-4270}
UI_PORT=${UI_PORT:-4271}
PG_DB=${PG_DB:-walk_sched_tmpl}
SIM_URL="http://127.0.0.1:${SIM_PORT}"
API_URL="http://127.0.0.1:${UI_PORT}"

DB_FLAGS=()
if [ "$DB" = postgres ]; then
  DB_FLAGS=(-postgres-url "postgres://grain:grain@127.0.0.1:5432/${PG_DB}?sslmode=disable")
fi

if [ -n "${KEEP:-}" ]; then
  if [ -f "$WORK_ROOT/daemon.pid" ]; then kill "$(cat "$WORK_ROOT/daemon.pid")" 2>/dev/null || true; fi
  sleep 2
else
for f in daemon sim; do
  if [ -f "$WORK_ROOT/$f.pid" ]; then kill "$(cat "$WORK_ROOT/$f.pid")" 2>/dev/null || true; fi
done
sleep 1
rm -rf "$WORK_ROOT"
mkdir -p "$WORK_ROOT/repos" "$WORK_ROOT/data" "$WORK_ROOT/sb" "$WORK_ROOT/clones" "$WORK_ROOT/out"
if [ "$DB" = postgres ]; then
  PGPASSWORD=grain psql -h 127.0.0.1 -U grain -d postgres -qc "DROP DATABASE IF EXISTS ${PG_DB}" -c "CREATE DATABASE ${PG_DB}"
fi

setsid "$SIM_BIN" -addr "127.0.0.1:${SIM_PORT}" -repos-dir "$WORK_ROOT/repos" \
  -repos "acme/widgets,acme/upstream,acme/downstream" -token sim-token \
  >"$WORK_ROOT/sim.log" 2>&1 </dev/null &
echo $! >"$WORK_ROOT/sim.pid"
for _ in $(seq 50); do curl -fsS "$SIM_URL/_sim/state" >/dev/null 2>&1 && break; sleep 0.2; done

init_repo() {
  local repo=$1 dir="$WORK_ROOT/clones/${1//\//_}"
  mkdir -p "$dir"
  git -C "$dir" init -q -b main
  git -C "$dir" config user.name "Alice Example"
  git -C "$dir" config user.email "alice@example.com"
  git -C "$dir" remote add origin "http://sim-token:@127.0.0.1:${SIM_PORT}/${repo}.git"
}
for r in acme/upstream acme/downstream; do
  init_repo "$r"; d="$WORK_ROOT/clones/${r//\//_}"
  echo "$r" >"$d/README"; git -C "$d" add -A; git -C "$d" commit -q -m init; git -C "$d" push -q -u origin main
done

W="$WORK_ROOT/clones/acme_widgets"
init_repo acme/widgets
mkdir -p "$W/.grain/templates" "$W/.grain/schedules"
echo widgets >"$W/README"
cat >"$W/.grain/templates/plain.yml" <<'EOF'
title: Plain widgets sweep
body: Sweep the widgets.
EOF
cat >"$W/.grain/templates/big.yml" <<'EOF'
title: Build the big widget image
body: Needs a big disk.
sandbox:
  disk-gb: 60
EOF
cat >"$W/.grain/templates/nightly.yml" <<'EOF'
title: Nightly widgets sync
body: Sync widgets from upstream.
admission: durable
grants:
  - capability: https://grain.dev/capabilities/repo#read
    with: {repo: acme/upstream}
  - https://grain.dev/capabilities/api#memory
EOF
for s in held free big own; do
  tmpl=plain; [ "$s" = big ] && tmpl=big; [ "$s" = own ] && tmpl=nightly
  printf 'template: %s\nevery:\n  minutes: 10\n' "$tmpl" >"$W/.grain/schedules/$s.yml"
done
echo 'skip-when: publishing-held' >>"$W/.grain/schedules/held.yml"
cat >>"$W/.grain/schedules/own.yml" <<'EOF'
grants:
  - https://grain.dev/capabilities/api#ci-read
EOF
if [ -n "${LEGACY:-}" ]; then
  # A schema-139 build knows neither key, and refuses a file that says one.
  sed -i '/^sandbox:/,$d' "$W/.grain/templates/big.yml"
  sed -i '/^skip-when:/d' "$W/.grain/schedules/held.yml"
fi
git -C "$W" add -A && git -C "$W" commit -q -m "acme/widgets: templates and schedules for §8" && git -C "$W" push -q -u origin main

cat >"$WORK_ROOT/fake-agy.sh" <<'EOF'
#!/usr/bin/env bash
for arg in "$@"; do case "$arg" in --help) exit 0;; --version) echo 'fake agy'; exit 0;; models) exit 0;; esac; done
exit 0
EOF
chmod +x "$WORK_ROOT/fake-agy.sh"
fi

setsid "$GRAIN_BIN" daemon -data-dir "$WORK_ROOT/data" -ui-addr "127.0.0.1:${UI_PORT}" "${DB_FLAGS[@]}" \
  -sandbox-port-base=0 -host-sandboxes -sandbox-dir "$WORK_ROOT/sb" -reconcile -poll-interval 1s \
  -github-host "127.0.0.1:${SIM_PORT}" -github-insecure-http -agy-path "$WORK_ROOT/fake-agy.sh" \
  >>"$WORK_ROOT/daemon.log" 2>&1 </dev/null &
echo $! >"$WORK_ROOT/daemon.pid"
for _ in $(seq 120); do
  kill -0 "$(cat "$WORK_ROOT/daemon.pid")" 2>/dev/null || { tail -n 40 "$WORK_ROOT/daemon.log"; exit 1; }
  if [ -s "$WORK_ROOT/data/local-api-token" ] &&
    curl -fsS -H "Authorization: Bearer $(cat "$WORK_ROOT/data/local-api-token")" "$API_URL/api/config" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
TOK=$(cat "$WORK_ROOT/data/local-api-token")
api() { curl -sS -X "$1" -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" ${3:+--data "$3"} "$API_URL$2"; }
[ -n "${KEEP:-}" ] && { echo "daemon restarted: $API_URL"; exit 0; }
api PUT /api/github-tokens/default '{"value":"sim-token"}' >/dev/null
api PUT /api/config "{\"agentBackends\":[{\"id\":\"ag-walk\",\"name\":\"Walk Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$WORK_ROOT/fake-agy.sh\",\"models\":[\"fake-model\"]}]}" >/dev/null
api PUT /api/agent-backends/ag-walk/credential '{"value":"walk-fake-key-not-real"}' >/dev/null
[ "${PAUSE:-1}" = 0 ] || api POST /api/pipeline-pause '{"reason":"§8 walkthrough: hold every filed task in the queue"}' >/dev/null
for r in acme/upstream acme/downstream acme/widgets; do
  api POST /api/repos "{\"repo\":\"$r\"}" >/dev/null
  api PUT "/api/repos/${r%/*}/${r#*/}/ci-mode" '{"ciMode":"github"}' >/dev/null
done
api PUT /api/repos/acme/widgets/declarations '{"declarations":true}' >/dev/null
echo "environment up ($DB): $API_URL (token in $WORK_ROOT/data/local-api-token)"
