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

# §3 item 4: a store whose sequences a v3.8.0 rebuild left behind is repaired
# on HEAD's first start (one log line per sequence) and a second start
# rewinds nothing. The broken store is made by v3.8.0 itself: a HEAD store
# (schema 147) rolled back onto v3.8.0 (146) with v3.8.0's own
# `grain state prepare-upgrade`, so v3.8.0's start archives it and rebuilds a
# fresh store through its ImportMigrated, which predates the resync.
source "$(dirname "$0")/env.sh"
reset_db
reset_work

echo "== HEAD populates a store"
start_daemon "$GRAIN"
for i in 1 2 3 4; do writes "head-$i"; done
api POST /api/tasks/1/close '{}' | cut -c1-4
stop_daemon
seq_summary

echo "== v3.8.0 rolls the store back: prepare-upgrade, then start (its rebuild)"
"$GRAIN_OLD" state -data-dir "$WORK_DIR/data" prepare-upgrade 2>"$WORK_DIR/prepare-old.err" || { cat "$WORK_DIR/prepare-old.err"; exit 1; }
: >"$WORK_DIR/daemon.log"
start_daemon "$GRAIN_OLD"
grep -E "schema|sequence|imported" "$WORK_DIR/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-200 | sort -u || true
echo "-- sequences left by the v3.8.0 rebuild"
seq_summary
echo "-- v3.8.0 writes on its own rebuilt store (expected to hit the original bug)"
writes v380-after-rebuild || echo "v3.8.0 write failed as above"
stop_daemon
echo "-- what v3.8.0 logged for those failures"
grep -oE "SQLSTATE 23505[^\"]*|duplicate key value violates unique constraint \"[a-z_]+\"" "$WORK_DIR/daemon.log" | sort | uniq -c
cp "$WORK_DIR/daemon.log" "$WORK_DIR/daemon-v380.log"

echo "== HEAD first start on the v3.8.0-rebuilt store"
"$GRAIN" state -data-dir "$WORK_DIR/data" prepare-upgrade 2>"$WORK_DIR/prepare-head.err" || { cat "$WORK_DIR/prepare-head.err"; exit 1; }
: >"$WORK_DIR/daemon.log"
start_daemon "$GRAIN"
grep -E "schema|sequence" "$WORK_DIR/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-200 || true
echo "-- sequences after HEAD's first start"
seq_summary
seq_report > "$WORK_DIR/after-first.txt"
writes head-healed
stop_daemon
seq_report > "$WORK_DIR/before-second.txt"

echo "== HEAD second start: nothing to resync, nothing rewound"
: >"$WORK_DIR/daemon.log"
start_daemon "$GRAIN"
grep -cE "PostgreSQL sequence .* was behind" "$WORK_DIR/daemon.log" || true
seq_report > "$WORK_DIR/after-second.txt"
if diff "$WORK_DIR/before-second.txt" "$WORK_DIR/after-second.txt"; then echo "sequences identical across the second start"; fi
writes head-second
seq_summary
stop_daemon
