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

# walk_items_1_3.sh drives §10 items 1 (group across kinds, withdrawal,
# per-repository) and 3 (starved firing raised and cleared) on the
# environment env.sh started. Each step pulls next_run_at forward so the
# real reconciler fires the schedule on its next 1s tick.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
source "$HERE/helpers.sh"
W="$WORK_ROOT/clones/acme_widgets"
step() { echo; echo "=== $*"; }
wait_sync() { # wait_sync <sql-count> <want>
  for _ in $(seq 60); do [ "$(sqlite3 "$DB" "$1")" = "$2" ] && return 0; sleep 2; done
  echo "timed out waiting for: $1 = $2"; return 1
}
close_task() { api POST "/api/tasks/$1/close" '{"reason":"walkthrough"}' >/dev/null; }
open_ids() { sqlite3 "$DB" "SELECT group_concat(t.task_id) FROM task_state t JOIN task_tag g ON g.task_id=t.task_id WHERE t.state<>'closed' AND g.tag='$1';"; }

wait_sync "SELECT count(*) FROM schedule WHERE overlap_group='train'" 4

step "1a. all three kinds due at once in acme/widgets, plus acme/other's same-named group"
due; sleep 4; snapshot 4
A_TASK=$(open_ids 'schedule-group:acme/widgets:train')
echo "acme/widgets group holder task: $A_TASK; acme/other group task: $(open_ids 'schedule-group:acme/other:train')"

step "3a. starved: b-suite's held occurrence 25m old (3 occurrences) behind never-started #$A_TASK"
sqlite3 "$DB" "UPDATE task SET priority=7 WHERE id='$(open_ids 'schedule-group:acme/other:train')'; UPDATE schedule SET next_run_at = strftime('%Y-%m-%d %H:%M:%S +0000 UTC','now','-25 minutes') WHERE id='repo:schedule:acme:widgets:b-suite';"
sleep 4
db "SELECT id, priority FROM task WHERE id='$A_TASK'"
db "SELECT id, subject, task_id, title, cleared_at FROM notification WHERE family='pipeline' OR subject LIKE '%starved%'"

step "1b/3b. close #$A_TASK: the starved notice clears, b-suite's held occurrence fires a suite run, c-workflow is held by it"
close_task "$A_TASK"; sleep 5; snapshot 3
db "SELECT id, cleared_at, cleared_by FROM notification WHERE subject LIKE '%starved%'"
SUITE_TASK=$(sqlite3 "$DB" "SELECT task_id FROM suite_run_task ORDER BY id DESC LIMIT 1")
db "SELECT task_id, tag FROM task_tag WHERE task_id='$SUITE_TASK'"

step "1c. withdraw b-suite while its run is active; a-task and c-workflow come due"
git -C "$W" rm -q .grain/schedules/b-suite.yml
git -C "$W" commit -q -m "withdraw b-suite"
git -C "$W" push -q origin main
wait_sync "SELECT count(*) FROM schedule WHERE id='repo:schedule:acme:widgets:b-suite'" 0
db "SELECT id, schedule_id, status FROM suite_run"
due "id IN ('repo:schedule:acme:widgets:a-task','repo:schedule:acme:widgets:c-workflow')"; sleep 4; snapshot 3
echo "open tasks in acme/widgets' group: $(open_ids 'schedule-group:acme/widgets:train') (want only suite task $SUITE_TASK)"

step "1d. close the suite task: the run finishes and the group passes on"
close_task "$SUITE_TASK"; sleep 5; snapshot 3
