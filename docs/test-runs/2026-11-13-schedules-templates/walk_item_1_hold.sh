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

# walk_item_1_hold.sh is §8 item 1: `skip-when: publishing-held`. It seeds
# the base-branch publishing finding for acme/widgets main (three failed
# releases, failing for four hours) and opens a release triage task
# carrying base-release-triage:acme/widgets:main, which gates it. held
# and free come due; held is skipped, free fires. Closing the triage task
# lets held fire once on the next tick.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
source "$HERE/helpers.sh"
GRAIN=${GRAIN_BIN:-/home/debian/bin/grain}

step "1a. no hold: held and free both fire"
due held free; sleep 4
sched_tasks held 3; sched_tasks free 3

close_open held free

step "1b. raise the hold: seed base_branch_release and open a triage task"
dbv "INSERT INTO base_branch_release (owner, name, branch, \"commit\", run_id, started_at, finished_at, conclusion, failures, failing_since, attempts)
     VALUES ('acme','widgets','main','deadbeef','run-3', $(ts 600), $(ts 300), 'failure', 3, $(ts 14400), 3)" >/dev/null
TRIAGE=$(api POST /api/tasks '{"repo":"acme/widgets","title":"Triage: acme/widgets main releases publish nothing","description":"seeded triage","approve":true}' | jq -r '.id // .task.id')
dbv "INSERT INTO task_tag (task_id, tag) VALUES ('$TRIAGE', 'base-release-triage:acme/widgets:main')" >/dev/null
echo "triage task: $TRIAGE"
api GET /api/sandboxes | jq '.baseBranchPublishing'

step "1c. held and free come due under the hold"
due held free; sleep 4
sched_tasks held 3; sched_tasks free 3
db "SELECT schedule_id, outcome, reason, task_id, detail FROM scheduled_firing_skip ORDER BY id DESC LIMIT 3"
echo "--- /api/schedules (held, free)"
api GET /api/schedules | jq '[.[]? // .schedules[]? | select(.id|test("acme:widgets:(held|free)$")) | {id, nextRunAt, suppressed, lastSkip: (.lastSkip // .skips // null)}]'
echo "--- grain health"
"$GRAIN" -server "$API_URL" -token "$TOK" health || echo "(grain health exited $?)"
echo "--- grain health -json | .baseBranchPublishing"
"$GRAIN" -server "$API_URL" -token "$TOK" health -json 2>/dev/null | jq '.baseBranchPublishing' || true

close_open free

step "1d. a second tick under the hold re-skips without a second record"
sleep 4
db "SELECT count(*) AS skips FROM scheduled_firing_skip WHERE schedule_id='repo:schedule:acme:widgets:held'"

step "1e. close the triage task: held fires once on the next tick"
api POST "/api/tasks/$TRIAGE/close" '{"reason":"walkthrough: publishing fixed"}' >/dev/null
sleep 5
sched_tasks held 3
db "SELECT schedule_id, outcome, reason, task_id, resolved_at FROM scheduled_firing_skip ORDER BY id DESC LIMIT 3"
api GET /api/schedules | jq '[.[]? // .schedules[]? | select(.id|test("acme:widgets:held$")) | {id, nextRunAt, suppressed}]'
