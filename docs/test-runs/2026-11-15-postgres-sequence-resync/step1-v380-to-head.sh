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

# §3 item 1: v3.8.0 on PostgreSQL with tasks, comments, events, audit rows and
# suite items; then HEAD through (a) the real v3.8.0 -> HEAD upgrade the
# setup.sh path takes (`grain state prepare-upgrade`, then start) and (b) a
# forced schema-change rebuild (store archived, fresh store filled by
# staterepo.ImportMigrated); writes after each, sequences recorded throughout.
source "$(dirname "$0")/env.sh"
reset_db
reset_work

echo "== v3.8.0 populates the store"
start_daemon "$GRAIN_OLD"
for i in 1 2 3 4 5; do writes "v380-$i"; done
api POST /api/tasks/2/close '{}' | cut -c1-4
api POST /api/tasks/3/approve '{}' | cut -c1-4
stop_daemon
echo "-- sequences after v3.8.0 (before HEAD)"
seq_summary

echo "== (a) HEAD: grain state prepare-upgrade, then start"
"$GRAIN" state -data-dir "$WORK_DIR/data" prepare-upgrade 2>"$WORK_DIR/prepare.err" || { cat "$WORK_DIR/prepare.err"; exit 1; }
start_daemon "$GRAIN"
grep -E "schema|sequence" "$WORK_DIR/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | sort -u || true
echo "-- sequences after the HEAD start (before writes)"
seq_summary
writes head-a1
writes head-a2
echo "-- sequences after HEAD writes"
seq_summary
stop_daemon

echo "== (b) forced rebuild: the store stamped one schema ahead of HEAD (a rollback from a"
echo "   newer build), HEAD's prepare-upgrade writes the note, HEAD's start archives the store and"
echo "   rebuilds a fresh one with staterepo.ImportMigrated"
ahead=$(( $(psql_db -c 'SELECT version FROM grain_schema WHERE id = 1') + 1 ))
psql_db -c "UPDATE grain_schema SET version = $ahead WHERE id = 1"
"$GRAIN" state -data-dir "$WORK_DIR/data" prepare-upgrade 2>"$WORK_DIR/prepare-b.err" || { cat "$WORK_DIR/prepare-b.err"; exit 1; }
: >"$WORK_DIR/daemon.log"
start_daemon "$GRAIN"
grep -E "schema|sequence|imported" "$WORK_DIR/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-220 | sort -u || true
echo "-- archived schemas"
psql_db -c "SELECT nspname FROM pg_namespace WHERE nspname LIKE 'grain_archive%' OR nspname LIKE '%schema%' ORDER BY 1"
echo "-- sequences in the rebuilt store (before writes)"
seq_summary
writes head-b1
writes head-b2
echo "-- sequences after writes on the rebuilt store"
seq_summary
stop_daemon
