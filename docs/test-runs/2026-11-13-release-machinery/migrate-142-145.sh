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

# Walkthrough 2026-11-13 §9 item 3: a real store created by a schema-139
# daemon (built from 24d0eba1), seeded with the bare-owner rows a build
# before migrations 142 and 145 wrote, then opened by a daemon built from
# HEAD. Prints every owner afterwards.
#
#   bash migrate-142-145.sh sqlite|postgres <root>
#
# GRAIN_OLD (default /home/debian/bin/grain-139) and GRAIN (default
# /home/debian/bin/grain) name the two binaries. For postgres,
# PGURL (default postgres://grain:grain@127.0.0.1:5432/<db>) names the
# server; a fresh database is created for each run.
set -euo pipefail
driver="${1:?sqlite|postgres}"
root="${2:?root}"
old="${GRAIN_OLD:-/home/debian/bin/grain-139}"
new="${GRAIN:-/home/debian/bin/grain}"
port="${PORT:-4431}"
rm -rf "$root" && mkdir -p "$root/data" "$root/sandbox"

dbargs=()
if [ "$driver" = postgres ]; then
  db="mig_$(date +%s)"
  PGPASSWORD=grain psql -h 127.0.0.1 -U grain -d postgres -qc "CREATE DATABASE $db" >/dev/null
  url="${PGURL:-postgres://grain:grain@127.0.0.1:5432/$db?sslmode=disable}"
  dbargs=(-postgres-url "$url")
  sql() { PGPASSWORD=grain psql -h 127.0.0.1 -U grain -d "$db" -qAt -F'|' -c "$1"; }
else
  sql() { sqlite3 -separator '|' "$root/data/store/grain.db" "$1"; }
fi

run_daemon() { # binary log
  "$1" daemon -data-dir "$root/data" -sandbox-dir "$root/sandbox" -sandbox-port-base=0 \
    -disable-native-sandboxes -disable-self-upgrade -ui-addr "127.0.0.1:$port" \
    -github-host 127.0.0.1:9 -github-insecure-http "${dbargs[@]}" >"$2" 2>&1 </dev/null &
  pid=$!
  up() { [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/healthz")" != 000 ]; }
  for _ in $(seq 1 240); do up && break; sleep 0.25; done
  up || { echo "daemon did not come up"; tail -20 "$2"; exit 1; }
  # Let the start-up migrations and seeding finish before stopping it.
  sleep 3
  kill "$pid"; wait "$pid" 2>/dev/null || true
}

echo "== $driver: create the store with $("$old" version 2>/dev/null | head -1)"
run_daemon "$old" "$root/old.log"
echo "schema before: $(sql 'SELECT version FROM grain_schema')"

# Target repos: acme/widgets only on the GHE host (moves), acme/gadgets
# on both hosts (ambiguous: stays), acme/tools only on github.com (stays).
sql "UPDATE grain_config SET target_repos = 'ghe.example.com/acme/widgets,acme/gadgets,ghe.example.com/acme/gadgets,acme/tools'"
now="2026-11-13T04:00:00Z"
i=0
for name in widgets gadgets tools; do
  i=$((i + 1))
  sql "INSERT INTO qualification_config (owner,name,require_approval,auto_promote) VALUES ('acme','$name',true,false)"
  sql "INSERT INTO qualification_item (owner,name,template_id,repeat_count,order_key) VALUES ('acme','$name','template-unit',1,0)"
  sql "INSERT INTO qualification_item_depends_on (owner,name,template_id,depends_on_template_id) VALUES ('acme','$name','template-unit','template-build')"
  sql "INSERT INTO scheduled_workflow_run (schedule_id,owner,name,fired_at) VALUES ('sched-gone-$name','acme','$name','$now')"
  sql "INSERT INTO release (id,owner,repo,name,status,created_at) VALUES ($i,'acme','$name','myfeat','active','$now')"
  sql "INSERT INTO release_candidate (id,release_id,owner,repo,number,branch,status,created_at) VALUES ($i,$i,'acme','$name',1,'release/myfeat-rc.1','active','$now')"
  sql "INSERT INTO qualification_run (id,owner,name,candidate_id,created_at) VALUES ($i,'acme','$name',$i,'$now')"
done

echo "== $driver: open it with $("$new" version 2>/dev/null | head -1)"
run_daemon "$new" "$root/new.log"
echo "schema after: $(sql 'SELECT version FROM grain_schema')"
for t in qualification_config qualification_item qualification_item_depends_on scheduled_workflow_run qualification_run; do
  echo "$t: $(sql "SELECT name || '=' || owner FROM $t ORDER BY name" | tr '\n' ' ')"
done
for t in release release_candidate; do
  echo "$t: $(sql "SELECT repo || '=' || owner FROM $t ORDER BY repo" | tr '\n' ' ')"
done
echo "migration log lines:"
grep -E "schema 14[0-9]|migrated the store" "$root/new.log" | head -20 || true
echo "forge reads the HEAD daemon made for the migrated repositories:"
grep -oE '(GET |Get ")https?://[^ "]+' "$root/new.log" | sed 's/?.*//' | sort | uniq -c || true
