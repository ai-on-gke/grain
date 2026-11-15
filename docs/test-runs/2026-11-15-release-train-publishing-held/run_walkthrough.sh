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

# docs/test-plans/2026-11-15.md §5 against a real daemon:
#   STORE=sqlite   ./run_walkthrough.sh /var/tmp/walk-rt-sqlite
#   STORE=postgres ./run_walkthrough.sh /var/tmp/walk-rt-postgres
# Leaves the daemon running (stop_env <dir> stops it) and writes every
# capture under <dir>/captures.

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

init_env "${1:-/var/tmp/walk-rt-${STORE:-sqlite}}"
start_ghsim
start_daemon
C="$WORK_DIR/captures"
SHA=$(cat "$WORK_DIR/seed.sha")
RT=repo:schedule:acme:grain:release-train
S1=repo:schedule:acme:grain:release-train-on-stage1
V351=repo:schedule:acme:grain:walk-release-v351
V351T=repo:schedule:acme:grain:walk-release-v351-template

say() { printf '\n== %s (%s)\n' "$*" "$(date -u +%T)"; }
iso() { date -u -d "$1" +%FT%TZ; }
gotime() { date -u -d "$1" '+%Y-%m-%d %H:%M:%S +0000 UTC'; }
run_json() { # id name path branch conclusion created
	printf '{"id":%s,"name":"%s","path":"%s","headBranch":"%s","headSha":"%s","event":"push","status":"completed","conclusion":"%s","createdAt":"%s","updatedAt":"%s"}' \
		"$1" "$2" "$3" "$4" "$SHA" "$5" "$6" "$6"
}
RUNS=()
add_run() { RUNS=("$(run_json "$@")" "${RUNS[@]}"); seed_runs "[$(IFS=,; echo "${RUNS[*]}")]" >/dev/null; }
tasks() { api GET /api/tasks | jq -r '.[] | [.id, .state, .title] | @tsv'; }
skips() {
	store_q "select id, schedule_id, occurrence, reason, task_id, outcome, fired_task_id, held_by_schedule_id from scheduled_firing_skip order by id"
}
schedules() {
	store_q "select id, next_run_at, last_run_at from schedule where id in ('$RT','$S1','$V351','$V351T') order by id"
}
# overdue moves the cadence schedules' next run 65 minutes into the past:
# three missed 20-minute ticks.
overdue() {
	local at
	if [[ ${STORE:-sqlite} == sqlite ]]; then at="'$(gotime '-65 min')'"; else at="now() - interval '65 minutes'"; fi
	store_q "update schedule set next_run_at = $at where id in ('$RT','$V351','$V351T')"
}
snapshot() { # label
	{ echo "-- tasks"; tasks; echo "-- skips"; skips; echo "-- schedules"; schedules; } >"$C/$1.txt" 2>&1
	cat "$C/$1.txt"
}

say "0. dispatch paused, acme/grain on ci_mode github"
api POST /api/pipeline-pause '{"reason":"walkthrough: keep filed tasks open"}' | jq -c '.pausedNow'
api PUT /api/repos/acme/grain/ci-mode '{"ciMode":"github"}' | jq -c '{ciMode}'

say "1a. three failed release.yml runs on main (the real input: recordGitHubBaseBranchRelease)"
add_run 9001 Release .github/workflows/release.yml main failure "$(iso '-30 min')"
add_run 9002 Release .github/workflows/release.yml main failure "$(iso '-20 min')"
add_run 9003 Release .github/workflows/release.yml main failure "$(iso '-10 min')"
for _ in $(seq 60); do
	if api GET /api/sandboxes | jq -e '.baseBranchPublishing[]? | select(.gated)' >/dev/null 2>&1; then break; fi
	sleep 0.5
done
api GET /api/sandboxes | jq '.baseBranchPublishing' | tee "$C/1a-finding.json"
TRIAGE=$(jq -r '.[0].triageTaskID' "$C/1a-finding.json")
echo "triage task: $TRIAGE"
grain_cli health >"$C/1a-health.txt" 2>&1 || true
cat "$C/1a-health.txt"

say "1b. declarations on: this repository's .grain/ syncs"
for _ in $(seq 10); do
	api PUT /api/repos/acme/grain/declarations '{"declarations":true}' >/dev/null && break
	sleep 1
done
for _ in $(seq 60); do
	if store_q "select id from schedule where id = '$RT'" | grep -q "$RT"; then break; fi
	sleep 0.5
done
grep -E "declares|declarations:" "$WORK_DIR/daemon.log" | tail -n 2 || true
store_q "select id, base, recurrence_kind, every_n_minutes, overlap, overlap_group, skip_when from schedule where id in ('$RT','$S1','$V351','$V351T') order by id"

say "1c. three cadence ticks overdue while held; Stage 1 ticks while held"
overdue
sleep 10
add_run 9101 Nightly .github/workflows/nightly.yml v3.9.0-rc.1 success "$(iso now)"
sleep 10
add_run 9102 Nightly .github/workflows/nightly.yml v3.9.0-rc.2 failure "$(iso now)"
sleep 25
snapshot 1c-held
api GET /api/schedules >"$C/1c-schedules.json"

if [[ -n ${PAUSE_FOR_SCREENSHOTS:-} ]]; then
	say "held: shoot screenshots now, then touch $WORK_DIR/continue"
	while [[ ! -f "$WORK_DIR/continue" ]]; do sleep 1; done
fi

say "2a. triage closed in the same cycle a Stage 1 run concludes"
add_run 9103 Nightly .github/workflows/nightly.yml v3.9.0-rc.3 success "$(iso now)"
api POST "/api/tasks/$TRIAGE/close" '{"reason":"walkthrough: publisher repaired"}' >/dev/null
sleep 15
snapshot 2a-released
grain_cli health >"$C/2a-health.txt" 2>&1 || true

say "2b. still one firing after further cycles"
sleep 20
snapshot 2b-settled

say "2c. the group's tasks closed: the next Stage 1 run fires the trigger"
# Every open task but the director's console (task 1) is the group's or the
# release/v3.5.1 tick's: close them all.
for id in $(api GET /api/tasks | jq -r '.[] | select(.state != "closed" and .id != "1") | .id'); do
	api POST "/api/tasks/$id/close" '{"reason":"walkthrough: tick done"}' >/dev/null
	echo "closed $id"
done
sleep 5
add_run 9104 Nightly .github/workflows/nightly.yml v3.9.0-rc.4 success "$(iso now)"
sleep 15
snapshot 2c-resumed
api GET /api/tasks >"$C/tasks.json"
grep -E "orchestrator: (schedule|filed)" "$WORK_DIR/daemon.log" >"$C/daemon-schedule-lines.txt" || true
cat "$C/daemon-schedule-lines.txt"
