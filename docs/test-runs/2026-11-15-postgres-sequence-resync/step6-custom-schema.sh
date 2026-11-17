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

# §3 item 6: a daemon whose DSN sets search_path to a custom schema resyncs
# that schema's sequences only. Both schemas of one database hold a store with
# every sequence put behind; the daemon on `walk` must log and fix walk.* and
# leave public.* exactly as it was.
source "$(dirname "$0")/env.sh"
reset_db
reset_work
WALK_DSN="${PG_DSN}&search_path=walk"

echo "== a store in public, its sequences put behind"
start_daemon "$GRAIN"
for i in 1 2 3; do writes "public-$i"; done
stop_daemon
break_sequences public

echo "== a store in schema walk (search_path=walk), its sequences put behind"
psql_db -c "CREATE SCHEMA walk"
WALK_DIR=${WORK_DIR}-walk
rm -rf "$WALK_DIR"
mkdir -p "$WALK_DIR/data"
PG_DSN=$WALK_DSN DATA_DIR=$WALK_DIR/data start_daemon "$GRAIN"
for i in 1 2 3 4 5; do writes "walk-$i"; done
stop_daemon
break_sequences walk
echo "-- public before"
seq_summary public | tee "$WORK_DIR/public-before.txt"
echo "-- walk before"
seq_summary walk

echo "== daemon on search_path=walk"
: >"$WORK_DIR/daemon.log"
PG_DSN=$WALK_DSN DATA_DIR=$WALK_DIR/data start_daemon "$GRAIN"
grep -E "sequence|advisory lock \(schema|acquired" "$WORK_DIR/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-200 || true
echo "-- walk after"
seq_summary walk
writes walk-after
echo "-- public after (must be unchanged)"
seq_summary public | tee "$WORK_DIR/public-after.txt"
diff "$WORK_DIR/public-before.txt" "$WORK_DIR/public-after.txt" && echo "public.* untouched"
stop_daemon
