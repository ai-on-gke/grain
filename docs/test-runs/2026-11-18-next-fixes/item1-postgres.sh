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

# Item 1 of docs/test-plans/2026-11-18.md §8 over PostgreSQL: a schedule
# bound to a template with a different base (and one with an empty base)
# fires into the template's base, and a publishing hold reads held, then
# fired. Uses the 2026-11-13 harness (STORE=postgres) on its own ports.
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
export STORE=postgres DAEMON_PORT=${DAEMON_PORT:-4281} GHSIM_PORT=${GHSIM_PORT:-4285}
source "$HERE/../2026-11-13-grain-health-exit-codes/env.sh"
W=/tmp/walk-next-pg
init_env "$W"
start_ghsim "$W"
start_daemon "$W" -agy-path "$HERE/fake-agent.sh"
psqlq() { PGPASSWORD=grain psql -qtA -h 127.0.0.1 -U grain -d grain_walk -c "$1"; }

t=$(mktemp -d)
git clone -q "$W/widgets.git" "$t/w"
git -C "$t/w" checkout -qb release
echo rel >"$t/w/RELEASE"
git -C "$t/w" add . && git -C "$t/w" -c user.name=s -c user.email=s@example.com commit -qm "chore: release only"
git -C "$t/w" push -q origin release
echo "release=$(git -C "$t/w" rev-parse --short HEAD) main=$(git -C "$t/w" rev-parse --short origin/main)"

api POST /api/templates '{"name":"Bound to release","title":"Template bound to release","description":"Fire into release.","repo":"acme/widgets","base":"release"}' >/dev/null
api POST /api/templates '{"name":"Bound default","title":"Template bound to default branch","description":"Fire into default branch.","repo":"acme/widgets","base":""}' >/dev/null
api POST /api/schedules '{"templateId":"template-1","repo":"acme/widgets","base":"main","recurrence":{"kind":"everyNMinutes","everyNMinutes":60},"overlap":"skip"}' >/dev/null
api POST /api/schedules '{"templateId":"template-2","repo":"acme/widgets","base":"release","recurrence":{"kind":"everyNMinutes","everyNMinutes":60},"overlap":"skip"}' >/dev/null

stale() { psqlq "update schedule set base='stale-main', next_run_at=now() - interval '30 minutes' where id='sched-1'; update schedule set base='release', next_run_at=now() - interval '30 minutes' where id='sched-2';"; }
stale
echo "api: $(api GET /api/schedules | jq -c '[(if type=="array" then . else .schedules end)[] | select(.id|startswith("sched-")) | {id,base}]')"
sleep 25
api GET '/api/tasks?limit=20' | jq -c '(if type=="array" then . else .tasks end)[] | select(.title|startswith("Template bound")) | {id,title,base,state}'
for f in "$W"/runs/checkout-*.txt; do echo "$(basename "$f"): $(grep -o 'head-commit=.*' "$f")"; done

# Publishing hold on release.
id=$(api POST /api/tasks '{"title":"Triage release failing releases","description":"walk triage","repo":"acme/widgets","base":"release"}' | jq -r .id)
psqlq "insert into task_tag values ('$id','base-release-triage:acme/widgets:release')"
psqlq "insert into base_branch_release (owner,name,branch,\"commit\",run_id,started_at,finished_at,conclusion,failures,failing_since) values ('acme','widgets','release','x','rel-12',now() - interval '4 hours',now() - interval '3 hours','failure',12,now() - interval '17 hours') on conflict do nothing"
psqlq "update schedule set skip_when='publishing-held', next_run_at=now() - interval '5 minutes' where id='sched-1'"
sleep 15
echo "held: $(api GET /api/schedules | jq -c '(if type=="array" then . else .schedules end)[] | select(.id=="sched-1") | {suppressed:.suppressed.detail,skips:[.skips[]?|{reason,outcome}]}')"
api POST "/api/tasks/$id/close" '{"reason":"triage done"}' >/dev/null
sleep 20
echo "after close: $(api GET /api/schedules | jq -c '(if type=="array" then . else .schedules end)[] | select(.id=="sched-1") | {suppressed,skips:[.skips[]?|{reason,outcome,firedTaskId}]}')"
echo "CODE=$CODE"
