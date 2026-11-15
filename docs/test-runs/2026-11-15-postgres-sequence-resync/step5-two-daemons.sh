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

# §3 item 5: a second daemon on the same PostgreSQL database never moves
# sequences while the first holds the single-writer lock; the active daemon
# keeps allocating. Two sequences the API writes below do not touch
# (task_event, board_sequence) are put behind by hand while daemon A runs, so a
# resync by anything but the lock holder would show.
source "$(dirname "$0")/env.sh"
reset_db
reset_work
B_DIR=${WORK_DIR}-b
rm -rf "$B_DIR"
mkdir -p "$B_DIR/data"

echo "== daemon A (active)"
start_daemon "$GRAIN"
TOKEN_A=$TOKEN
for i in 1 2 3; do writes "a-$i"; done
api POST /api/tasks/1/close '{}' | cut -c1-4
api POST /api/boards '{"name":"b1"}' | cut -c1-4
api POST /api/boards '{"name":"b2"}' | cut -c1-4
psql_db -c "SELECT setval('task_event_id_seq', 1, false), setval('board_sequence_id_seq', 1, false)" >/dev/null
echo "-- sequences with two put behind by hand"
seq_summary

echo "== daemon B (same database, its own data directory and port), waiting on the lock"
DAEMON_SUFFIX=-b DAEMON_PORT=4291 DATA_DIR=$B_DIR/data start_daemon "$GRAIN" &
B_WAIT=$!
sleep 12  # AcquireWriterLock waits 30s (DefaultWriterLockWaitTimeout) for the lock
grep -E "lock|sequence|refusing" "$WORK_DIR/daemon-b.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-200 || true
psql_db -c "SELECT 'lock holders: ' || string_agg(application_name, ', ') FROM pg_stat_activity WHERE datname = '$PG_DB' AND application_name LIKE 'grain-daemon-writer-lock%'"
echo "-- sequences while B waits (task_event and board_sequence must still be BEHIND)"
seq_summary
echo "-- A keeps allocating"
TOKEN=$TOKEN_A writes a-while-b-waits

echo "== A loses its lease: its lock connection is terminated, B takes the lock over"
psql_db -c "SELECT count(pg_terminate_backend(pid)) || ' lock connection(s) of A terminated' FROM pg_stat_activity WHERE datname = '$PG_DB' AND application_name LIKE 'grain-daemon-writer-lock%' AND pid IN (SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted)"
wait "$B_WAIT" || true
sleep 3
echo "-- A's log"
grep -E "lock|sequence|lost" "$WORK_DIR/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-200 | tail -6 || true
echo "-- B's log"
grep -E "lock|sequence" "$WORK_DIR/daemon-b.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-200 || true
kill -0 "$(cat "$WORK_DIR/daemon.pid" 2>/dev/null)" 2>/dev/null && echo "A still running" || echo "A exited"
echo "-- sequences after B became the writer"
seq_summary
TOKEN=$(tr -d '[:space:]' <"$B_DIR/data/local-api-token") DAEMON=http://127.0.0.1:4291 writes b-active
stop_daemon -b
stop_daemon
