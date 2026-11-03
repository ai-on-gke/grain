#!/bin/sh
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

# Item 6: a Postgres store written at schema 85 (grain built at 75331e14,
# the audited range's start) upgraded by this build (schema 93), with two of
# the columns 86..93 add pre-created to simulate a partial earlier run:
# task.origin_actor_source (migration 91, the first of its list of ALTERs)
# and task_run_capability_audit.hostd (migration 93's last statement).
# Postgres is Debian's postgresql-15 (`apt-get install postgresql`).
set -u
OLD=/home/debian/bin/grain-s85
NEW=/home/debian/bin/grain
DB=grain85
URL="postgres://grain:grain@127.0.0.1:5432/$DB?sslmode=disable"
W=$(mktemp -d)
sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS $DB" -c "DO \$\$BEGIN CREATE ROLE grain LOGIN PASSWORD 'grain'; EXCEPTION WHEN duplicate_object THEN NULL; END\$\$" -c "CREATE DATABASE $DB OWNER grain"
psql_g() { PGPASSWORD=grain psql -h 127.0.0.1 -U grain -d "$DB" -At "$@"; }

daemon() { # binary port datadir
  HOME=$W setsid "$1" daemon -data-dir "$3" -postgres-url "$URL" -ui-addr "127.0.0.1:$2" \
    -sandbox-backend host -sandbox-dir "$3/sandboxes" -sandbox-port-base=0 \
    -github-host 127.0.0.1:1 -github-insecure-http >"$3.log" 2>&1 </dev/null &
  echo $!
}
wait_up() { for _ in $(seq 1 90); do curl -s -o /dev/null "http://127.0.0.1:$1/" && return 0; sleep 1; done; return 1; }

echo "== schema-85 daemon"
P=$(daemon "$OLD" 18722 "$W/old")
wait_up 18722 || { tail -20 "$W/old.log"; exit 1; }
T=$(cat "$W/old/local-api-token")
api() { curl -s -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@"; }
api http://127.0.0.1:18722/api/schedules -d '{"title":"Nightly CI review","description":"Triage a red nightly.yml run.","repo":"acme/widgets","base":"main","recurrence":{"kind":"github_workflow_run","githubWorkflowRun":{"workflow":"nightly.yml","conclusions":["failure"],"branches":["main"]}}}' | jq -c '{id,title}'
api http://127.0.0.1:18722/api/tasks -d '{"title":"pre-upgrade task","description":"x","noRepo":true}' | jq -c '{id,title}'
api http://127.0.0.1:18722/api/tasks/2/comments -d '{"body":"a comment written at 85"}' -o /dev/null -w 'comment %{http_code}\n'
sleep 3
kill "$P"; sleep 3
echo "schema_version before: $(psql_g -c 'SELECT max(version) FROM schema_version' 2>/dev/null || psql_g -c '\dt' | head -3)"

echo "== pre-create two columns migrations 91 and 93 add"
psql_g -c 'ALTER TABLE task ADD COLUMN origin_actor_source TEXT NOT NULL DEFAULT '"''"
psql_g -c "CREATE TABLE IF NOT EXISTS task_run_capability_audit (id BIGSERIAL PRIMARY KEY)" 2>&1 | head -2
psql_g -c 'ALTER TABLE task_run_capability_audit ADD COLUMN hostd TEXT NOT NULL DEFAULT '"''" 2>&1 | head -2

echo "== this build (schema 93) on the same database"
P=$(daemon "$NEW" 18723 "$W/old")
if ! wait_up 18723; then echo "new daemon did not come up:"; grep -i 'migrat\|schema\|error' "$W/old.log" | tail -15; exit 1; fi
grep -i 'migrat' "$W/old.log" | tail -5
T=$(cat "$W/old/local-api-token")
curl -s -H "Authorization: Bearer $T" http://127.0.0.1:18723/api/schedules | jq -c '(.[]? // .schedules[]?) | select(.title=="Nightly CI review") | {id,title,recurrence}'
curl -s -H "Authorization: Bearer $T" http://127.0.0.1:18723/api/tasks/2 | jq -c '{title:.task.title, comments:[.comments[]?.body]}'
echo "columns: $(psql_g -c "SELECT string_agg(table_name||'.'||column_name, ' ') FROM information_schema.columns WHERE column_name IN ('origin_actor_source','hostd','author_source') AND table_schema='public'")"
psql_g -c "SELECT 'hostd rows: '||count(*) FROM hostd" 2>&1
psql_g -c "SELECT 'merge_batch columns: '||string_agg(column_name, ',') FROM information_schema.columns WHERE table_name='merge_batch'" 2>&1
kill "$P"
