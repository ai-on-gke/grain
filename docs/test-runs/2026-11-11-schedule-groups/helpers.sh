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

# helpers.sh is sourced by the step scripts: `api`, `db`, `due` and
# `snapshot` against the environment env.sh started.
WORK_ROOT=${WORK_ROOT:-/var/tmp/walk-sched-groups}
API_URL=${API_URL:-http://127.0.0.1:4261}
DB="$WORK_ROOT/data/store/grain.db"
TOK=$(cat "$WORK_ROOT/data/local-api-token")
api() { curl -sS -X "$1" -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" ${3:+--data "$3"} "$API_URL$2"; }
db() { sqlite3 -header -column "$DB" "$@"; }
# due makes every schedule in acme/widgets' group come due now, so the
# next 1s reconcile tick fires them in id order.
due() { sqlite3 "$DB" "UPDATE schedule SET next_run_at = strftime('%Y-%m-%d %H:%M:%S +0000 UTC','now','-1 seconds') WHERE ${1:-overlap_group <> ''};"; }
# snapshot prints what is unfinished in every group and the latest skips.
snapshot() {
  echo "--- open schedule tasks"
  db "SELECT t.id, t.target_owner||'/'||t.target_name AS repo, t.title, t.priority, (SELECT group_concat(tag) FROM task_tag g WHERE g.task_id=t.id) AS tags, s.state FROM task t JOIN task_state s ON s.task_id=t.id WHERE s.state NOT IN ('closed') AND t.id IN (SELECT task_id FROM task_tag WHERE tag LIKE 'schedule:%') ORDER BY t.id;" 2>&1
  echo "--- active suite runs"
  db "SELECT id, schedule_id, status FROM suite_run WHERE status='active';" 2>&1
  echo "--- unsettled scheduled workflow runs"
  db "SELECT schedule_id, run_id, fired_at FROM scheduled_workflow_run WHERE finished_at IS NULL;" 2>&1
  echo "--- last skips"
  db "SELECT schedule_id, outcome, reason, task_id, held_by_schedule_id, substr(detail,1,110) AS detail FROM scheduled_firing_skip ORDER BY id DESC LIMIT ${1:-6};" 2>&1
}
