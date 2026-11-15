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

# §3 item 2: a populated SQLite store carried onto PostgreSQL with
# `grain db upgrade-postgres` (postgres.UpgradeFromSQLite), then normal writes.
source "$(dirname "$0")/env.sh"
reset_db
reset_work

echo "== HEAD on SQLite populates the store"
STORE=sqlite start_daemon "$GRAIN"
for i in 1 2 3 4 5 6; do writes "sqlite-$i"; done
api POST /api/tasks/2/close '{}' | cut -c1-4
stop_daemon
sqlite3 "$WORK_DIR/data/store/grain.db" "SELECT 'sqlite max task_comment.id=' || MAX(id) FROM task_comment; SELECT 'sqlite max config_audit.id=' || MAX(id) FROM config_audit; SELECT 'sqlite max suite_item.id=' || MAX(id) FROM suite_item;" 2>/dev/null || ls "$WORK_DIR/data/store"
echo "-- postgres before the upgrade (empty database)"
seq_summary

echo "== grain db upgrade-postgres"
# GRAIN_SANDBOX_CLI=0: inside a grain sandbox `grain db` is otherwise the in-sandbox command.
GRAIN_SANDBOX_CLI=0 "$GRAIN" db upgrade-postgres -data-dir "$WORK_DIR/data" -dsn "$PG_DSN" 2>&1 | tail -5
echo "-- sequences after UpgradeFromSQLite"
seq_summary

echo "== HEAD on PostgreSQL, writes"
: >"$WORK_DIR/daemon.log"
start_daemon "$GRAIN"
grep -E "sequence" "$WORK_DIR/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' || echo "(no sequence resync logged on start)"
writes pg-1
writes pg-2
echo "-- sequences after writes"
seq_summary
stop_daemon
