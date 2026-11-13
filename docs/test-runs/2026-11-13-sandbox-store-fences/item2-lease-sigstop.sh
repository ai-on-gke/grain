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

# §12 item 2: PostgreSQL single-writer lease fencing under SIGSTOP.
# Daemons A and B play two VMs (separate data dirs) on one PostgreSQL
# database. A is the leader and is writing; it is frozen with SIGSTOP for
# longer than any lease interval, B is started to take over, and A is then
# resumed. Pass EVICT=1 to also terminate A's frozen sessions from the
# database side (what PostgreSQL's own idle_session_timeout or TCP
# keepalives would do for a dead peer) before B starts.
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib.sh"
STOP_FOR=${STOP_FOR:-20}
EVICT=${EVICT:-0}
DB=walk12_i2; URL=$(pgurl $DB)
pg_up
rm -rf "$W/i2-"*; recreate_db $DB
dbnow() { psql_g $DB -c "SELECT clock_timestamp()"; }

say "2a. daemon A takes the lease and starts writing"
A=$(daemon i2-a 4294 "$W/i2-a" -postgres-url "$URL")
wait_up 4294 || { tail -30 "$W/i2-a.log"; exit 1; }
grep -h "single-writer" "$W/i2-a.log"
FT=$(api "$W/i2-a" 4294 /api/tasks -d '{"title":"fence target","description":"x","noRepo":true}' | jq -r .id)
echo "fence target task: $FT"
writer A "$W/i2-a" 4294 "$FT" 300 >"$W/i2-writes-a.log" 2>&1 &
WA=$!
sleep 3
psql_g $DB -c "SELECT pid, application_name, state FROM pg_stat_activity WHERE datname='$DB' AND application_name LIKE 'grain-daemon-writer-lock%'"

say "2b. SIGSTOP A for ${STOP_FOR}s (keep-alive 5s, re-acquire window 15s)"
kill -STOP "$A"; TSTOP=$(dbnow); echo "A stopped at $TSTOP (database clock)"
sleep "$STOP_FOR"
psql_g $DB -c "SELECT 'A sessions still open: '||count(*)||', advisory locks held: '||(SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND granted) FROM pg_stat_activity WHERE datname='$DB' AND backend_type='client backend' AND pid <> pg_backend_pid()"
if [ "$EVICT" = 1 ]; then
  echo "evicting A's sessions from the database side"
  psql_g $DB -c "SELECT count(*)||' terminated' FROM (SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$DB' AND backend_type='client backend' AND pid <> pg_backend_pid()) t"
fi

say "2c. daemon B starts while A is frozen"
T0=$(date +%s)
B=$(daemon i2-b 4296 "$W/i2-b" -postgres-url "$URL")
for _ in $(seq 1 45); do
  grep -q "acquired PostgreSQL single-writer" "$W/i2-b.log" && break
  kill -0 "$B" 2>/dev/null || break; sleep 1
done
if grep -q "acquired PostgreSQL single-writer" "$W/i2-b.log"; then
  TB=$(dbnow); echo "B acquired the lease after $(( $(date +%s) - T0 ))s (database clock $TB)"
else
  TB=""; echo "B did not acquire the lease within $(( $(date +%s) - T0 ))s; B alive=$(kill -0 "$B" 2>/dev/null && echo yes || echo no)"
fi
grep -h "single-writer\|another grain daemon" "$W/i2-b.log" | tail -3 | cut -c1-400

say "2d. resume A"
kill -CONT "$A"; TCONT=$(dbnow); echo "A resumed at $TCONT"
if wait_gone "$A" 40; then echo "A exited after resuming"; else echo "A still running 40s after resuming"; fi
grep -h "single-writer\|fencing\|lost\|write refused" "$W/i2-a.log" | tail -6 | cut -c1-300
if [ -n "$TB" ]; then
  wait_up 4296 && echo "B serving on 4296"
  writer B "$W/i2-b" 4296 "$FT" 10 >"$W/i2-writes-b.log" 2>&1
fi
kill $WA 2>/dev/null; wait $WA 2>/dev/null

say "2e. what reached the database"
echo "A writer status codes: $(awk '{print $3}' "$W/i2-writes-a.log" | sort | uniq -c | tr '\n' ' ')"
[ -f "$W/i2-writes-b.log" ] && echo "B writer status codes: $(awk '{print $3}' "$W/i2-writes-b.log" | sort | uniq -c | tr '\n' ' ')"
psql_g $DB -c "SELECT split_part(body,' ',1)||' rows: '||count(*)||', last at '||max(created_at) FROM task_comment WHERE task_id='$FT' AND body LIKE '_ write %' GROUP BY split_part(body,' ',1) ORDER BY split_part(body,' ',1)"
if [ -n "$TB" ]; then
  echo "A rows committed after B took the lease: $(psql_g $DB -c "SELECT count(*) FROM task_comment WHERE task_id='$FT' AND body LIKE 'A write %' AND created_at > '$TB'")"
  echo "A rows committed after A was frozen: $(psql_g $DB -c "SELECT count(*) FROM task_comment WHERE task_id='$FT' AND body LIKE 'A write %' AND created_at > '$TSTOP'")"
  echo "rows written by both (same body twice): $(psql_g $DB -c "SELECT count(*) FROM (SELECT body FROM task_comment WHERE task_id='$FT' GROUP BY body HAVING count(*)>1) d")"
fi
OKA=$(awk '$3==200' "$W/i2-writes-a.log" | wc -l)
echo "A writes acknowledged 200: $OKA; A rows in the database: $(psql_g $DB -c "SELECT count(*) FROM task_comment WHERE task_id='$FT' AND body LIKE 'A write %'")"
kill "$A" "$B" 2>/dev/null; wait_gone "$A" 20; wait_gone "$B" 20
