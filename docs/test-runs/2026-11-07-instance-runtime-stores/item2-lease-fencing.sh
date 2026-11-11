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

# §2 item 2: PostgreSQL single-writer lease, split-brain write fencing and
# the DB-persisted GitHub webhook cursor (#740). Daemons A, B and C play
# three VMs (separate data dirs) on one PostgreSQL database; a real
# `github-webhook serve` gateway (cmd/github-webhook) holds the events.
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib.sh"
GW=${GW:-/home/debian/bin/github-webhook}
DB=walk_i2; URL=$(pgurl $DB)
rm -rf "$W/i2-"*; recreate_db $DB
export GITHUB_WEBHOOK_SECRET=whsec-walk GITHUB_WEBHOOK_RELAY_SECRET=relay-walk
cursor() { psql_g $DB -c "SELECT coalesce((SELECT last_acked_seq FROM github_webhook_cursor),-1)"; }
hook() { # gateway-port seq
  local body; body=$(printf '{"action":"opened","issue":{"number":%d,"title":"walk %d","body":"x","user":{"login":"octo"}},"repository":{"full_name":"acme/widgets","private":true},"sender":{"login":"octo"}}' "$2" "$2")
  local sig; sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$GITHUB_WEBHOOK_SECRET" | awk '{print $NF}')
  curl -s -X POST "http://127.0.0.1:$1/webhooks/github" -H "X-GitHub-Event: issues" -H "X-GitHub-Delivery: walk-$2" \
    -H "X-Hub-Signature-256: sha256=$sig" -H 'Content-Type: application/json' -d "$body"; echo
}
wait_cursor() { for _ in $(seq 1 60); do [ "$(cursor)" = "$1" ] && return 0; sleep 1; done; return 1; }

say "2a. gateway + daemon A (gateway mode), three events"
setsid "$GW" serve -addr 127.0.0.1:4297 -store "$W/i2-gw-events.jsonl" >"$W/i2-gw.log" 2>&1 </dev/null &
GWP=$!; sleep 1
A=$(daemon i2-a 4294 "$W/i2-a" -postgres-url "$URL" -github-webhook-url http://127.0.0.1:4297)
wait_up 4294 || { tail -30 "$W/i2-a.log"; exit 1; }
grep -h "single-writer" "$W/i2-a.log"
for s in 1 2 3; do hook 4297 $s; done
wait_cursor 3 && echo "github_webhook_cursor after A: $(cursor)"
ls "$W/i2-a/github-webhook-cursor.json" 2>/dev/null || echo "no github-webhook-cursor.json in A's data dir (cursor lives in PostgreSQL)"

say "2b. daemon B on the same database while A holds the lease"
T0=$(date +%s)
B=$(daemon i2-b 4296 "$W/i2-b" -postgres-url "$URL" -github-webhook-url http://127.0.0.1:4297)
wait_gone "$B"; echo "B exited after $(( $(date +%s) - T0 ))s; A alive=$(kill -0 "$A" && echo yes)"
grep -h "single-writer\|another grain daemon\|ERROR\|error" "$W/i2-b.log" | tail -3 | cut -c1-400
curl -s -o /dev/null -w "B's UI port: %{http_code}\n" http://127.0.0.1:4296/ || true
psql_g $DB -c "SELECT application_name, state FROM pg_stat_activity WHERE application_name LIKE 'grain-daemon-writer-lock%'"

say "2c. lease loss under active writes: terminate A's lock session while B waits"
api "$W/i2-a" 4294 /api/tasks -d '{"title":"fence target","description":"x","noRepo":true}' | jq -c '{id,title}'
FT=$(api "$W/i2-a" 4294 /api/tasks | jq -r '.[] | select(.title=="fence target") | .id')
( i=0; while [ $i -lt 400 ]; do i=$((i+1)); c=$(curl -s -o /dev/null -w '%{http_code}' -m 3 -H "Authorization: Bearer $(tok "$W/i2-a")" -H 'Content-Type: application/json' "http://127.0.0.1:4294/api/tasks/$FT/comments" -d "{\"body\":\"A write $i\"}"); echo "$(date +%s.%N) $i $c"; sleep 0.05; done ) >"$W/i2-writes.log" 2>&1 &
WL=$!
sleep 2
B=$(daemon i2-b 4296 "$W/i2-b" -postgres-url "$URL" -github-webhook-url http://127.0.0.1:4297)
sleep 3
echo "terminating A's writer-lock backend at $(date -u +%T.%N)"
TT=$(psql_g $DB -c "SELECT clock_timestamp() FROM (SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='grain-daemon-writer-lock' AND pid = (SELECT min(pid) FROM pg_stat_activity WHERE application_name='grain-daemon-writer-lock')) t")
echo "A's lock session terminated at $TT (database clock)"
wait_gone "$A"; echo "A exited (alive=$(kill -0 "$A" 2>/dev/null && echo yes || echo no))"
kill $WL 2>/dev/null; wait $WL 2>/dev/null
grep -h "single-writer\|fencing\|lost" "$W/i2-a.log" | tail -4 | cut -c1-300
wait_up 4296 && grep -h "acquired PostgreSQL single-writer" "$W/i2-b.log"
echo "writer loop status codes: $(awk '{print $3}' "$W/i2-writes.log" | sort | uniq -c | tr '\n' ' ')"
OKS=$(awk '$3==200' "$W/i2-writes.log" | wc -l); LAST=$(awk '$3==200{n=$2}END{print n}' "$W/i2-writes.log")
INDB=$(psql_g $DB -c "SELECT count(*) FROM task_comment WHERE task_id='$FT' AND body LIKE 'A write %'")
echo "A writes acknowledged 200: $OKS (last #$LAST); A writes in the database: $INDB"
psql_g $DB -c "SELECT 'max committed A write: '||max(substr(body,9)::int)||' at '||max(created_at) FROM task_comment WHERE task_id='$FT' AND body LIKE 'A write %'"
echo "A writes committed after its lease was gone: $(psql_g $DB -c "SELECT count(*)||' ('||coalesce(string_agg(substr(body,9), ',' ORDER BY created_at),'none')||')' FROM task_comment WHERE task_id='$FT' AND body LIKE 'A write %' AND created_at > '$TT'")"
grep -h "acquired PostgreSQL single-writer" "$W/i2-b.log" | awk '{print "B acquired at "$1" "$2}'
grep -h "write refused" "$W/i2-a.log" | head -2 | cut -c1-300
awk '$3!=200{print "first failed A write: #"$2" at "strftime("%H:%M:%S", $1)" code "$3; exit}' "$W/i2-writes.log"

say "2d. cursor handoff to B (fresh data dir, gateway mode): no replay, then advance"
grep -h "webhook" "$W/i2-b.log" | head -3 | cut -c1-300
echo "cursor before event 4: $(cursor)"
hook 4297 4
wait_cursor 4 && echo "cursor after B consumed event 4: $(cursor)"
grep -h "GET /v1/events\|after=" "$W/i2-gw.log" | tail -2 | cut -c1-200

say "2e. daemon C, a replacement VM in local-listener mode: DB cursor ahead of an empty local store"
kill "$B"; wait_gone "$B"
C=$(daemon i2-c 4298 "$W/i2-c" -postgres-url "$URL" -github-webhook-addr 127.0.0.1:4299)
wait_up 4298 || { tail -20 "$W/i2-c.log"; }
grep -h "ahead of local store" "$W/i2-c.log" | cut -c1-300
hook 4299 5
wait_cursor 1 && echo "cursor after C's first local event (local seq 1): $(cursor)"
kill "$C" "$GWP"; wait_gone "$C"
