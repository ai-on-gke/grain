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

# walk_item_4_upgrade.sh is §8 item 4: a store written by a schema-139
# build (24d0eba1) upgraded in place to HEAD through migrations 140..146,
# on DB=sqlite or DB=postgres. Build the old binary first:
#   git worktree add /var/tmp/old-grain 24d0eba1 &&
#   (cd /var/tmp/old-grain && go build -o /home/debian/bin/grain-139 ./cmd/grain)
#
# Seeded on 139:
#   own  (template nightly: durable, api#memory): fired once by the old
#        build, which copies the template's admission and grants onto the
#        schedule row; its file adds api#ci-read. An operator-written copy
#        of api#memory is added in SQL too.
#   free (template plain: nothing): raised in SQL to bypass + console and
#        given an operator grant api#memory -- all its own difference.
#   big  (template big: nothing on 139): raised in SQL to durable.
# After HEAD starts: own holds no admission and no copy; free and big keep
# their raises; the re-read sync then writes own's declared api#ci-read
# back (and, being the file's, drops the operator grant seeded on free).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
DB=${DB:-sqlite}
export DB WORK_ROOT=/var/tmp/walk-sched-upgrade-$DB SIM_PORT=4272 UI_PORT=4273 API_URL=http://127.0.0.1:4273 PG_DB=walk_sched_upgrade
OLD=${OLD:-/home/debian/bin/grain-139}

echo "=== schema 139 daemon ($DB)"
LEGACY=1 GRAIN_BIN="$OLD" "$HERE/env.sh"
# shellcheck source=/dev/null
source "$HERE/helpers.sh"
wait_sync "SELECT count(*) FROM schedule WHERE id LIKE 'repo:schedule:acme:widgets:%'" 4
due own; sleep 5
echo "--- own after the 139 build fired it"
db "SELECT id, admission, hosting FROM schedule WHERE id='repo:schedule:acme:widgets:own'"
db "SELECT schedule_id, capability, via FROM schedule_grant WHERE schedule_id LIKE 'repo:%' ORDER BY schedule_id, capability"

echo "--- seed the rest in SQL"
dbv "INSERT INTO schedule_grant (schedule_id, capability, via) VALUES ('repo:schedule:acme:widgets:own', 'https://grain.dev/capabilities/api#memory', 'operator') ON CONFLICT DO NOTHING" >/dev/null
dbv "UPDATE schedule SET admission='bypass', hosting='console' WHERE id='repo:schedule:acme:widgets:free'" >/dev/null
dbv "INSERT INTO schedule_grant (schedule_id, capability, via) VALUES ('repo:schedule:acme:widgets:free', 'https://grain.dev/capabilities/api#memory', 'operator')" >/dev/null
dbv "UPDATE schedule SET admission='durable' WHERE id='repo:schedule:acme:widgets:big'" >/dev/null
db "SELECT id, template_id, admission, hosting FROM schedule WHERE id LIKE 'repo:%' ORDER BY id"
db "SELECT schedule_id, capability, via FROM schedule_grant WHERE schedule_id LIKE 'repo:%' ORDER BY schedule_id, capability"
db "SELECT version FROM schema_version" 2>/dev/null || true

echo "=== upgrade in place to HEAD"
KEEP=1 "$HERE/env.sh"
sleep 3
grep -E "migrat" "$WORK_ROOT/daemon.log" | tail -n 5 | cut -c1-300
db "SELECT id, template_id, admission, hosting, skip_when FROM schedule WHERE id LIKE 'repo:%' ORDER BY id"
db "SELECT schedule_id, capability, via FROM schedule_grant WHERE schedule_id LIKE 'repo:%' ORDER BY schedule_id, capability"
db "SELECT id, sandbox FROM template WHERE id LIKE 'repo:%' ORDER BY id"

echo "=== migration 144 forgot each repository's last-read commit: the next sync (within the 2-minute poll) re-reads"
db "SELECT owner, name, \"commit\", last_error FROM repo_declaration_sync"
START=$(date +%s)
wait_sync "SELECT count(*) FROM schedule_grant WHERE schedule_id='repo:schedule:acme:widgets:own' AND via='.grain/schedules/own.yml'" 1 || true
echo "own's declared grant back after $(( $(date +%s) - START ))s"
db "SELECT schedule_id, capability, via FROM schedule_grant WHERE schedule_id LIKE 'repo:%' ORDER BY schedule_id, capability"

echo "=== own fires on HEAD: template's durable + api#memory, plus its own api#ci-read"
close_open own; due own; sleep 5
ID=$(dbv "SELECT task_id FROM task_tag WHERE tag='schedule:repo:schedule:acme:widgets:own' ORDER BY CAST(task_id AS INTEGER) DESC LIMIT 1")
db "SELECT id, admission, hosting FROM task WHERE id='$ID'"
db "SELECT capability, via FROM task_grant WHERE task_id='$ID' ORDER BY capability"
