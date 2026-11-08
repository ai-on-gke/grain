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

# §2 item 1: offline SQLite -> PostgreSQL cutover, stale-store refusal,
# change-token polling on PostgreSQL, `grain backup` (pg_dump) and a
# restore into a fresh database with row parity.
# Needs gcs-stub (go run ./gcsstub) on 127.0.0.1:4293 for the backup upload.
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib.sh"
D=$W/i1-data
rm -rf "$D" "$W/i1-"*
DB=walk_i1; RESTORE=walk_i1_restore
recreate_db $DB; recreate_db $RESTORE

say "1a. seed a SQLite store through a live daemon"
P=$(daemon i1-sqlite 4291 "$D")
wait_up 4291 || { tail -20 "$W/i1-sqlite.log"; exit 1; }
for i in 1 2 3; do api "$D" 4291 /api/tasks -d "{\"title\":\"seed task $i\",\"description\":\"seeded on sqlite\",\"noRepo\":true}" | jq -c '{id,title}'; done
api "$D" 4291 /api/tasks/2/comments -d '{"body":"a comment written on sqlite"}' -o /dev/null -w 'comment %{http_code}\n'
api "$D" 4291 /api/schedules -d '{"title":"Nightly CI review","description":"triage","repo":"acme/widgets","base":"main","recurrence":{"kind":"github_workflow_run","githubWorkflowRun":{"workflow":"nightly.yml","conclusions":["failure"],"branches":["main"]}}}' | jq -c '{id,title}'
api "$D" 4291 /api/secrets/walk-secret/token -X PUT -d '{"value":"s3cr3t-value-1"}' -o /dev/null -w 'secret put %{http_code}\n'
api "$D" 4291 /api/hostds -d '{"name":"ws-walk"}' | jq -c '{name:(.name // .hostd.name), tokenShown:((.token // "")|length>0)}'
sleep 6 # let the high-water mark move

say "1b. grain upgrade-postgres while the SQLite daemon is attached (expect refusal)"
"$GRAIN" upgrade-postgres -data-dir "$D" -dsn "$(pgurl $DB)"; echo "exit=$?"
echo "tables in $DB after refusal: $(psql_g $DB -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")"

say "1c. stop the daemon and migrate offline"
kill "$P"; wait_gone "$P"
"$GRAIN" upgrade-postgres -data-dir "$D" -dsn "$(pgurl $DB)"; echo "exit=$?"
echo "db-config.json: $(jq -c . "$D/db-config.json")"
grep -c "$PGPASS" "$D/db-config.json" | sed 's/^/password occurrences in db-config.json: /'

say "1d. boot grain daemon on PostgreSQL (db-config.json selects it)"
P=$(daemon i1-pg 4291 "$D")
wait_up 4291 || { tail -30 "$W/i1-pg.log"; exit 1; }
grep -E "store|lease|writer lock" "$W/i1-pg.log" | head -5
api "$D" 4291 /api/tasks/2 | jq -c '{title, comments:[.comments[]?.body]}'
api "$D" 4291 /api/secrets | jq -c '[.secrets[] | select(.name|startswith("walk") or .=="database-password") | {name, keys}]'
api "$D" 4291 /api/hostds | jq -c '[.[]? // .hostds[]? | .name]'

say "1e. change token: ETag of GET /api/tasks advances on API and on out-of-process commits"
etag() { curl -s -D - -o /dev/null -H "Authorization: Bearer $(tok "$D")" http://127.0.0.1:4291/api/tasks | awk -F': ' 'tolower($1)=="etag"{print $2}' | tr -d '\r'; }
E1=$(etag); E1b=$(etag); echo "etag twice with no write: $E1 / $E1b (equal=$([ "$E1" = "$E1b" ] && echo yes || echo no))"
api "$D" 4291 /api/tasks -d '{"title":"written on postgres","description":"x","noRepo":true}' | jq -c '{id,title}'
E2=$(etag); echo "after POST /api/tasks: $E2 (changed=$([ "$E1" != "$E2" ] && echo yes || echo no))"
psql_g $DB -c "UPDATE task SET title='renamed out of process' WHERE id='2'"
E3=$(etag); echo "after psql UPDATE from another process: $E3 (changed=$([ "$E2" != "$E3" ] && echo yes || echo no))"
api "$D" 4291 /api/tasks | jq -r '.[] | select(.id=="2") | .title' | sed 's/^/task 2 title via API: /'
psql_g $DB -c "UPDATE task SET title='seed task 1' WHERE id='2'"

say "1f. serialized read-write transactions: 20 concurrent comment writers"
for i in $(seq 1 20); do api "$D" 4291 /api/tasks/3/comments -d "{\"body\":\"concurrent $i\"}" -o /dev/null -w '%{http_code}\n' & done | sort | uniq -c
wait
echo "comments on task 3: $(api "$D" 4291 /api/tasks/3 | jq '[.comments[]? | select(.body|startswith("concurrent"))] | length')"
grep -ci "could not serialize\|deadlock" "$W/i1-pg.log" | sed 's/^/serialization errors in log: /'
sleep 6

say "1g. point the daemon back at the stale SQLite file (expect refusal)"
kill "$P"; wait_gone "$P"
cat "$D/store-highwater.json"
P=$(daemon i1-stale 4291 "$D" -db-driver sqlite -db-dsn "$D/store")
wait_gone "$P"; echo "stale daemon exited (pid gone=$(kill -0 "$P" 2>/dev/null && echo no || echo yes))"
grep -m1 "refusing to start" "$W/i1-stale.log" | fold -w 200

say "1h. grain backup (pg_dump) into the GCS stub, offline"
GRAIN_BACKUP_GCS_ENDPOINT=http://127.0.0.1:4293/storage/v1/ "$GRAIN" backup -data-dir "$D" -gcs-bucket walk-backups; echo "exit=$?"
curl -s "http://127.0.0.1:4293/storage/v1/b/walk-backups/o" | jq -r '.items[]?.name' | tail -1 > "$W/i1-backup-name"
N=$(cat "$W/i1-backup-name"); echo "uploaded: $N"
curl -s -o "$W/i1-backup.zip" "http://127.0.0.1:4293/download/storage/v1/b/walk-backups/o/$N?alt=media"
(cd "$W" && rm -rf i1-unzip && mkdir i1-unzip && cd i1-unzip && unzip -q ../i1-backup.zip && find . -type f | sort && cat manifest.json | jq -c .)

say "1i. restore into a fresh database and compare row counts table by table"
PGPASSWORD=$PGPASS "$PGBIN/pg_restore" -h 127.0.0.1 -p "$PGPORT" -U grain -d $RESTORE --no-owner "$W/i1-unzip/database/grain.dump"; echo "pg_restore exit=$?"
counts() { psql_g "$1" -c "SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' ORDER BY 1" | while read -r t; do echo "$t $(psql_g "$1" -c "SELECT count(*) FROM \"$t\"")"; done; }
counts $DB >"$W/i1-counts-src"; counts $RESTORE >"$W/i1-counts-restore"
echo "tables: $(wc -l <"$W/i1-counts-src") source, $(wc -l <"$W/i1-counts-restore") restored; rows: $(awk '{s+=$2}END{print s}' "$W/i1-counts-src") / $(awk '{s+=$2}END{print s}' "$W/i1-counts-restore")"
diff "$W/i1-counts-src" "$W/i1-counts-restore" && echo "row parity: identical"
for q in "SELECT string_agg(id||':'||title, ',' ORDER BY id) FROM task" "SELECT count(*)||' comments' FROM task_comment" "SELECT 'schema '||max(version) FROM grain_schema"; do
  echo "$q => src[$(psql_g $DB -c "$q" 2>&1)] restore[$(psql_g $RESTORE -c "$q" 2>&1)]"
done

say "1j. a daemon on the restored database"
D2=$W/i1-restore-data; mkdir -p "$D2"; cp -r "$D/secrets" "$D2/" 2>/dev/null
P=$(daemon i1-restore 4292 "$D2" -postgres-url "$(pgurl $RESTORE)")
wait_up 4292 && api "$D2" 4292 /api/tasks/2 | jq -c '{title, comments:[.comments[]?.body]}' || tail -20 "$W/i1-restore.log"
kill "$P"; wait_gone "$P"
