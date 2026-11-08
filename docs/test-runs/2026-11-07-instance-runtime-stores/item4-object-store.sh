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

# §2 item 4a: -object-store gs://... over gcsstub.go (127.0.0.1:4293).
# Daemon A runs a no-repo task with a stub `claude` that prints a
# deployment secret and writes a Claude session file; the transcript that
# reaches the bucket must be redacted, the session archived, and both
# readable back. Daemon B shares the bucket under another prefix.
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib.sh"
DB=walk_i4; URL=$(pgurl $DB)
GCS=$W/gcs; BUCKET=walk-objects
SECRET=wlk-deploy-s3cr3t-7f3a9c2e
rm -rf "$W/i4-"*; recreate_db $DB
curl -s "http://127.0.0.1:4293/_reset?bucket=$BUCKET"
export GRAIN_OBJECT_STORE_GCS_ENDPOINT=http://127.0.0.1:4293/storage/v1/

mkdir -p "$W/i4-stub"
cat >"$W/i4-stub/claude" <<EOF
#!/bin/sh
# stub claude: one tool call whose output carries the deployment secret
mkdir -p "\${CLAUDE_CONFIG_DIR:-\$HOME/.claude}/projects/walk"
echo '{"type":"session","note":"walk session bundle"}' > "\${CLAUDE_CONFIG_DIR:-\$HOME/.claude}/projects/walk/walk-session.jsonl"
echo '{"type":"system","subtype":"init","session_id":"walk-session"}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"run_command","input":{"command":"cat deploy.env"}}]}}'
echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"DEPLOY_TOKEN=$SECRET"}]}}'
sleep 4
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"the token was $SECRET; done"}]}}'
echo '{"type":"result","result":"done"}'
EOF
chmod +x "$W/i4-stub/claude"; echo sk-ant-oat01-stub >"$W/i4-stub/token"
cat >"$W/i4-conn.yaml" <<EOF
models:
  - id: claude
    framework: claude
    models: [claude-sonnet-5]
    credential_file: $W/i4-stub/token
default_model: claude-sonnet-5
EOF

say "4a. daemon A with -object-store gs://$BUCKET/instances/a/"
A=$(daemon i4-a 4301 "$W/i4-a" -postgres-url "$URL"); wait_up 4301; kill "$A"; wait_gone "$A"
HOME=$W/home-i4-a "$GRAIN" connections -data-dir "$W/i4-a" -db-driver postgres -db-dsn "$URL" import "$W/i4-conn.yaml" 2>&1 || \
  GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN="$URL" HOME=$W/home-i4-a "$GRAIN" connections -data-dir "$W/i4-a" import "$W/i4-conn.yaml"
A=$(daemon i4-a 4301 "$W/i4-a" -postgres-url "$URL" -object-store "gs://$BUCKET/instances/a/" -claude-path "$W/i4-stub/claude" -max-workers 1 -poll-interval 2s)
wait_up 4301 || { tail -30 "$W/i4-a.log"; exit 1; }
grep -h "object store\|object-store" "$W/i4-a.log" | head -4 | cut -c1-250
api "$W/i4-a" 4301 /api/secrets/deploy/token -X PUT -d "{\"value\":\"$SECRET\"}" -o /dev/null -w 'secret deploy/token put %{http_code}\n'
TID=$(api "$W/i4-a" 4301 /api/tasks -d '{"title":"print the deploy token","description":"print it","noRepo":true}' | jq -r .id)
api "$W/i4-a" 4301 "/api/tasks/$TID/approve" -X POST -o /dev/null -w "approve task $TID %{http_code}\n"
for _ in $(seq 1 90); do S=$(api "$W/i4-a" 4301 "/api/tasks/$TID" | jq -r .state); case $S in closed|awaiting_reply|failed|done) break;; esac; sleep 2; done
echo "task $TID state: $S"
sleep 12 # a periodic transcript sync

say "4b. what reached the bucket"
(cd "$GCS/$BUCKET" 2>/dev/null && find . -type f | sort | sed 's/^/  /')
echo "secret occurrences in bucket objects: $(grep -rl "$SECRET" "$GCS/$BUCKET" 2>/dev/null | wc -l)"
echo "secret occurrences in local raw transcripts: $(grep -rl "$SECRET" "$W/i4-a" 2>/dev/null | grep -v '/secrets/' | wc -l)"
grep -rho "DEPLOY_TOKEN=[^\"\\\\]*" "$GCS/$BUCKET" 2>/dev/null | sort -u | head -3

say "4c. read back through the API and the CLI"
RUN=$(api "$W/i4-a" 4301 "/api/tasks/$TID" | jq -r '.attempts[-1].number // 1')
api "$W/i4-a" 4301 "/api/tasks/$TID/attempts/$RUN/transcript" | head -c 600 | tr '\n' ' '; echo
echo "secret in API transcript: $(api "$W/i4-a" 4301 "/api/tasks/$TID/attempts/$RUN/transcript" | grep -c "$SECRET")"

say "4d. transcript survives the local cache being wiped (replacement VM)"
kill "$A"; wait_gone "$A"
rm -rf "$W/i4-a/cache/object-store"
find "$W/i4-a" -path '*transcripts*' -type f | head -5
A=$(daemon i4-a 4301 "$W/i4-a" -postgres-url "$URL" -object-store "gs://$BUCKET/instances/a/" -claude-path "$W/i4-stub/claude" -max-workers 1 -poll-interval 2s)
wait_up 4301
api "$W/i4-a" 4301 "/api/tasks/$TID/attempts/$RUN/transcript" -w '\n[%{http_code}]' | tail -c 300; echo

say "4e. fail closed: uploads to the bucket fail"
curl -s "http://127.0.0.1:4293/_fail?name=instances/a/"
TID2=$(api "$W/i4-a" 4301 /api/tasks -d '{"title":"print it again","description":"print it","noRepo":true}' | jq -r .id)
api "$W/i4-a" 4301 "/api/tasks/$TID2/approve" -X POST -o /dev/null -w "approve task $TID2 %{http_code}\n"
for _ in $(seq 1 60); do S=$(api "$W/i4-a" 4301 "/api/tasks/$TID2" | jq -r .state); case $S in closed|awaiting_reply|failed|done) break;; esac; sleep 2; done
echo "task $TID2 state with uploads failing: $S"
sleep 12
grep -h "object store\|objectstore\|transcript" "$W/i4-a.log" | grep -i "fail\|error\|503" | tail -3 | cut -c1-250
echo "task $TID2 transcript in the bucket while failing: $(ls "$GCS/$BUCKET/instances/a/transcripts/" | grep -c "^$TID2-") object(s)"
curl -s "http://127.0.0.1:4293/_fail?name="
sleep 15
echo "after the bucket recovers: $(ls "$GCS/$BUCKET/instances/a/transcripts/" | grep "^$TID2-" | tr '\n' ' ')"
echo "secret occurrences in bucket objects: $(grep -rl "$SECRET" "$GCS/$BUCKET" 2>/dev/null | wc -l)"
echo "task $TID2 narrative via the API (kept in the database instead): $(api "$W/i4-a" 4301 "/api/tasks/$TID2/attempts/1/transcript" | jq -r .transcript | tail -c 120 | tr '\n' ' ')"
echo "secret in any task_run row: $(psql_g $DB -c "SELECT count(*) FROM task_run WHERE transcript LIKE '%$SECRET%'")"
kill "$A"; wait_gone "$A"

say "4f. CI artifacts: daemons A and B share the bucket, same run/job/artifact name"
DBB=walk_i4b; recreate_db $DBB
for who in a b; do
  d=$W/i4-$who; mkdir -p "$d/state/ci-artifacts/run-77"
  echo "artifact written by daemon $who" >"$W/i4-art-$who.txt"
  tar -czf "$d/state/ci-artifacts/run-77/test.logs.tar.gz" -C "$W" "i4-art-$who.txt"
done
A=$(daemon i4-a 4301 "$W/i4-a" -postgres-url "$URL" -object-store "gs://$BUCKET/instances/a/")
B=$(daemon i4-b 4302 "$W/i4-b" -postgres-url "$(pgurl $DBB)" -object-store "gs://$BUCKET/instances/b/")
wait_up 4301; wait_up 4302; sleep 3
for who in a b; do
  f=$GCS/$BUCKET/instances/$who/ci-artifacts/run-77/test.logs.tar.gz
  echo "instances/$who/ci-artifacts/run-77/test.logs.tar.gz: $( [ -f "$f" ] && tar -xzOf "$f" || echo missing)"
done
kill "$A" "$B"; wait_gone "$A"; wait_gone "$B"
