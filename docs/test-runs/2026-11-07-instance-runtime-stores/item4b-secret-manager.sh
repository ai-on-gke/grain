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

# §2 item 4b: -secrets-backend=secretmanager over smstub.go (REST, on
# 127.0.0.1:4303) with the catalogue in PostgreSQL secret_entries; the
# daemon also runs -object-store over gcsstub.go so its transcript
# redaction has to read every value back out of Secret Manager.
# Run item4-object-store.sh first: it writes the stub agent this reuses.
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib.sh"
DB=walk_i4sm; URL=$(pgurl $DB); D=$W/i4sm
PREFIX=projects/walk-proj/secrets/grain-walk-
SECRET=wlk-deploy-s3cr3t-7f3a9c2e
rm -rf "$D" "$W/i4sm"*; recreate_db $DB
curl -s "http://127.0.0.1:4293/_reset?bucket=walk-sm"
export GRAIN_SECRET_MANAGER_ENDPOINT=http://127.0.0.1:4303 GRAIN_SECRET_MANAGER_TOKEN=fake-sm-token
export GRAIN_OBJECT_STORE_GCS_ENDPOINT=http://127.0.0.1:4293/storage/v1/
SMF=(-secrets-backend secretmanager -secrets-sm-prefix "$PREFIX")
smdump() { curl -s http://127.0.0.1:4303/_dump; }

say "4b-0. flag validation"
for args in "-secrets-sm-prefix $PREFIX" "-secrets-backend secretmanager" "-secrets-backend secretmanager -secrets-sm-prefix projects/walk-proj"; do
  P=$(daemon i4sm-bad 4304 "$W/i4sm-bad" -postgres-url "$URL" $args); wait_gone "$P"
  printf '%-75s -> %s\n' "$args" "$(tail -1 "$W/i4sm-bad.log" | cut -c21-220)"
done

say "4b-1. daemon with -secrets-backend=secretmanager"
P=$(daemon i4sm 4304 "$D" -postgres-url "$URL" "${SMF[@]}" -object-store gs://walk-sm/instances/sm/ \
  -claude-path "$W/i4-stub/claude" -max-workers 1 -poll-interval 2s)
wait_up 4304 || { tail -30 "$W/i4sm.log"; exit 1; }
grep -h -i "secret manager\|secretmanager\|secrets backend" "$W/i4sm.log" | head -3 | cut -c1-250
kill "$P"; wait_gone "$P"
ls "$D/db-config.json" 2>/dev/null || grep -h "recording the database" "$W/i4sm.log" | cut -c21-300
GRAIN_SECRETS_BACKEND=secretmanager GRAIN_SECRETS_SM_PREFIX=$PREFIX GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN="$URL" \
  HOME=$W/home-i4sm "$GRAIN" connections -data-dir "$D" import "$W/i4-conn.yaml"

say "4b-2. grain secrets set / list (CLI, daemon stopped)"
echo "-data-dir alone (no db-config.json):"
printf '%s' "$SECRET" | "$GRAIN" secrets -data-dir "$D" "${SMF[@]}" set deploy token; echo "exit=$? store created=$([ -e "$D/store" ] && echo yes || echo no)"
SMF+=(-db-driver postgres -db-dsn "$URL")
echo "with -db-driver postgres -db-dsn:"
printf '%s' "$SECRET" | "$GRAIN" secrets -data-dir "$D" "${SMF[@]}" set deploy token; echo "set exit=$?"
printf 'one' | "$GRAIN" secrets -data-dir "$D" "${SMF[@]}" set multi a; printf 'two' | "$GRAIN" secrets -data-dir "$D" "${SMF[@]}" set multi b
printf 'one-rotated' | "$GRAIN" secrets -data-dir "$D" "${SMF[@]}" set multi a
"$GRAIN" secrets -data-dir "$D" "${SMF[@]}" list
echo "secret_entries:"; psql_g $DB -c "SELECT secret_name||'/'||key||' -> '||sm_secret||' v'||coalesce(nullif(version,''),'1') FROM secret_entries ORDER BY 1" | sed 's/^/  /'
echo "Secret Manager (values base64):"; smdump | sed 's/^/  /'
echo "secret value in PostgreSQL (pg_dump grep): $(PGPASSWORD=$PGPASS "$PGBIN/pg_dump" -h 127.0.0.1 -p "$PGPORT" -U grain $DB | grep -c "$SECRET")"
echo "secret value under the data dir: $(grep -rl "$SECRET" "$D" 2>/dev/null | wc -l)"

say "4b-3. daemon reads: GET /api/secrets, PUT through the API, and a run whose transcript must be redacted from Secret Manager values"
P=$(daemon i4sm 4304 "$D" -postgres-url "$URL" "${SMF[@]}" -object-store gs://walk-sm/instances/sm/ \
  -claude-path "$W/i4-stub/claude" -max-workers 1 -poll-interval 2s)
wait_up 4304 || { tail -30 "$W/i4sm.log"; exit 1; }
api "$D" 4304 /api/secrets/ui-made/key -X PUT -d '{"value":"from-the-ui"}' -o /dev/null -w 'PUT /api/secrets/ui-made/key %{http_code}\n'
api "$D" 4304 /api/secrets | jq -c '[.secrets[] | select(.name|test("deploy|multi|ui-made")) | {name, keys}]'
TID=$(api "$D" 4304 /api/tasks -d '{"title":"print the deploy token","description":"print it","noRepo":true}' | jq -r .id)
api "$D" 4304 "/api/tasks/$TID/approve" -X POST -o /dev/null
for _ in $(seq 1 60); do S=$(api "$D" 4304 "/api/tasks/$TID" | jq -r .state); case $S in closed|awaiting_reply|failed) break;; esac; sleep 2; done
echo "task $TID: $S"; sleep 3
curl -s "http://127.0.0.1:4293/download/storage/v1/b/walk-sm/o/instances%2Fsm%2Ftranscripts%2F$TID-1.md?alt=media" | grep -o "DEPLOY_TOKEN=[^ ]* [^ ]* [^ ]*\]" | head -1 | sed 's/^/bucket transcript: /'
echo "secret in bucket: $(curl -s "http://127.0.0.1:4293/storage/v1/b/walk-sm/o" | jq -r '.items[].name' | while read -r o; do curl -s "http://127.0.0.1:4293/download/storage/v1/b/walk-sm/o/$(jq -rn --arg o "$o" '$o|@uri')?alt=media"; done | grep -c "$SECRET")"
grep -c "access" "$W/smstub.log" | sed 's/^/Secret Manager access calls so far: /'

say "4b-4. a secret deleted upstream behind grain's back (catalogue still lists it)"
curl -s -X POST "http://127.0.0.1:4303/_drop?id=$(psql_g $DB -c "SELECT sm_secret FROM secret_entries WHERE secret_name='multi' AND key='b'")"
kill "$P"; wait_gone "$P"   # drop the in-memory read cache
P=$(daemon i4sm 4304 "$D" -postgres-url "$URL" "${SMF[@]}" -object-store gs://walk-sm/instances/sm/ \
  -claude-path "$W/i4-stub/claude" -max-workers 1 -poll-interval 2s)
wait_up 4304
"$GRAIN" secrets -data-dir "$D" "${SMF[@]}" list | grep multi
TID=$(api "$D" 4304 /api/tasks -d '{"title":"print it with a secret missing upstream","description":"print it","noRepo":true}' | jq -r .id)
api "$D" 4304 "/api/tasks/$TID/approve" -X POST -o /dev/null
for _ in $(seq 1 60); do S=$(api "$D" 4304 "/api/tasks/$TID" | jq -r .state); case $S in closed|awaiting_reply|failed) break;; esac; sleep 2; done
echo "task $TID: $S"; sleep 3
grep -h "transcript" "$W/i4sm.log" | grep -i "secret\|redact" | tail -2 | cut -c1-260
echo "task $TID transcript objects in the bucket: $(curl -s "http://127.0.0.1:4293/storage/v1/b/walk-sm/o" | jq -r '.items[].name' | grep -c "/$TID-")"
echo "secret in task_run rows: $(psql_g $DB -c "SELECT count(*) FROM task_run WHERE transcript LIKE '%$SECRET%'")"
echo "task $TID narrative kept: $(api "$D" 4304 "/api/tasks/$TID/attempts/1/transcript" | jq -r '.transcript // "" | length') chars"

say "4b-5. grain secrets delete repairs the catalogue"
"$GRAIN" secrets -data-dir "$D" "${SMF[@]}" delete multi b; echo "delete multi b exit=$?"
"$GRAIN" secrets -data-dir "$D" "${SMF[@]}" delete deploy; echo "delete deploy exit=$?"
"$GRAIN" secrets -data-dir "$D" "${SMF[@]}" list | grep -E "multi|deploy|ui-made"
echo "secret_entries now: $(psql_g $DB -c "SELECT string_agg(secret_name||'/'||key, ' ' ORDER BY 1) FROM secret_entries WHERE secret_name IN ('multi','deploy','ui-made')")"
smdump | grep -c "grain-walk-" | sed 's/^/Secret Manager secrets left: /'
kill "$P"; wait_gone "$P"
