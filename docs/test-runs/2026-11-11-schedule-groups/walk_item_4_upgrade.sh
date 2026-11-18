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

# walk_item_4_upgrade.sh is §10 item 4: a store with existing schedules
# written by a pre-overlap-group build (schema 131, the parent of
# 6c0df5d19) upgraded in place to HEAD. Build the old binary first:
#   git worktree add /var/tmp/old-grain 6c0df5d19^ &&
#   (cd /var/tmp/old-grain && go build -o /home/debian/bin/grain-131 ./cmd/grain)
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
export WORK_ROOT=/var/tmp/walk-sched-upgrade SIM_PORT=4262 UI_PORT=4263 API_URL=http://127.0.0.1:4263
OLD=${OLD:-/home/debian/bin/grain-131}

echo "=== schema 131 daemon, legacy schedule files (overlap: skip, no group)"
LEGACY=1 GRAIN_BIN="$OLD" "$HERE/env.sh"
for _ in $(seq 60); do
  [ "$(sqlite3 "$WORK_ROOT/data/store/grain.db" "SELECT count(*) FROM schedule WHERE recurrence_kind='everyNMinutes' AND id LIKE 'repo:%'")" = 4 ] && break
  sleep 2
done
# shellcheck source=/dev/null
source "$HERE/helpers.sh"
due "id LIKE 'repo:%' AND recurrence_kind='everyNMinutes'"; sleep 8
db "SELECT id, last_run_at FROM schedule WHERE id LIKE 'repo:%'"
db "SELECT id, (SELECT group_concat(tag) FROM task_tag g WHERE g.task_id=t.id) AS tags FROM task t"
db "SELECT id, schedule_id, status FROM suite_run"

echo "=== upgrade in place to HEAD"
# libfaketime does not reach a Go binary's clock (time.Now reads the vDSO),
# so the cadence is pulled forward in SQL rather than with faketime.
KEEP=1 "$HERE/env.sh"
sleep 10
grep 'migrated the store' "$WORK_ROOT/daemon.log"
db "SELECT id, overlap, overlap_group FROM schedule WHERE id LIKE 'repo:%'"
due "id LIKE 'repo:%' AND recurrence_kind='everyNMinutes'"; sleep 8
db "SELECT id, last_run_at FROM schedule WHERE id LIKE 'repo:%' AND recurrence_kind='everyNMinutes'"
db "SELECT schedule_id, reason, task_id, held_by_schedule_id FROM scheduled_firing_skip"
db "SELECT id, conclusion FROM ci_run"
