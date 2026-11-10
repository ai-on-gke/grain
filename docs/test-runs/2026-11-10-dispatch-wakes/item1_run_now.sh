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

# Executes §9 Item 1: Run now through real suspensions + c08832a2 store upgrade
# + task-pane screenshots at 1440/390 in light and dark.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
# shellcheck source=docs/test-runs/2026-11-10-dispatch-wakes/env.sh
source "$HERE/env.sh"

cleanup() {
	chmod 0755 /tmp/walk-item1/sandboxes /tmp/walk-item1/sandboxes/runs /tmp/walk-upgrade/sandboxes /tmp/walk-upgrade/sandboxes/runs 2>/dev/null || true
	stop_env /tmp/walk-item1 2>/dev/null || true
	stop_env /tmp/walk-upgrade 2>/dev/null || true
}
trap cleanup EXIT

SHOT_ROOT=${SHOT_ROOT:-/tmp/walk-item1-shots}
rm -rf "$SHOT_ROOT"
mkdir -p "$SHOT_ROOT"

probe_ui() {
	PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node "$HERE/ui_probe.mjs" "$@"
}

shoot_pane() {
	local tid=$1 subdir=$2
	if [[ "${SKIP_SHOTS:-0}" == "1" ]]; then return 0; fi
	(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node e2e/screenshot.mjs \
		--url "$DAEMON" --code "$CODE" --pages "/tasks/$tid" --out "$SHOT_ROOT/$subdir" >/dev/null)
	echo "Screenshots saved to $SHOT_ROOT/$subdir"
}

dump_task_state() {
	local tid=$1 label=$2
	echo "=== [$label] Task $tid ==="
	echo "-- API summary:"
	api GET "/api/tasks/$tid" | jq '{
		id, state, admission, runNowAt, runNowStale, deferredUntil, pausedBy, activity, activityBySetup,
		attempts: [.attempts[] | {attempt, startedAt, finishedAt, outcome, suspendKind, detail}],
		comments: [.comments[] | {author, body}]
	}'
	echo "-- SQLite rows:"
	sqlite3 -json "$DB" "SELECT o.task_id, o.run_now_at, r.id AS run_id, r.started_at, r.finished_at, r.outcome, r.suspend_kind, s.kind AS susp_kind, s.resume_at AS susp_resume_at, s.resumed_at AS susp_resumed_at FROM task_observation o LEFT JOIN task_run r ON r.task_id = o.task_id LEFT JOIN task_suspension s ON s.run_id = r.id WHERE o.task_id = '$tid';"
	echo "-- UI pane probe:"
	probe_ui inspect "$DAEMON" "$CODE" "$tid"
}

echo "================================================================"
echo "PART A: Run now through real suspensions on HEAD daemon"
echo "================================================================"

init_env /tmp/walk-item1
start_ghsim /tmp/walk-item1
POLL_INTERVAL=400ms start_daemon /home/debian/bin/grain /tmp/walk-item1

# 1a. resume_later with at: 1h (triggered via UI Run now button click!)
T1=$(api POST /api/tasks '{"title":"Item1a: Run now -> resume_later 1h","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
api POST "/api/tasks/$T1/defer" '{}' >/dev/null
cat > "/tmp/walk-item1/action-$T1.json" <<'EOF'
[{"kind":"resume_later","mcp_args":{"reason":"waiting 1h after Run now","when":{"at":"1h"}}},{"kind":"nothing_needed","reason":"resumed run done"}]
EOF
echo "-- Clicking Run now in UI on deferred task $T1:"
probe_ui click "$DAEMON" "$CODE" "$T1" "Run now"
for _ in $(seq 50); do
	st=$(api GET "/api/tasks/$T1" | jq -r '.state + ":" + (.attempts | length | tostring)')
	[[ "$st" == "deferred:1" ]] && break
	sleep 0.3
done
dump_task_state "$T1" "1a: resume_later {at: 1h}"
shoot_pane "$T1" "1a-resume-later"

# 1c-std. Setup failure (injected via chmod 0555 sandboxes) on standard task
T3S=$(api POST /api/tasks '{"title":"Item1c-std: Run now -> setup-failed (standard)","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
api POST "/api/tasks/$T3S/defer" '{}' >/dev/null
chmod 0555 /tmp/walk-item1/sandboxes /tmp/walk-item1/sandboxes/runs 2>/dev/null || chmod 0555 /tmp/walk-item1/sandboxes
api POST "/api/tasks/$T3S/run-now" '' >/dev/null
for _ in $(seq 50); do
	cnt=$(api GET "/api/tasks/$T3S" | jq '.attempts | length')
	fin=$(api GET "/api/tasks/$T3S" | jq -r '.attempts[0].finishedAt // ""')
	[[ "$cnt" -ge 1 && -n "$fin" ]] && break
	sleep 0.3
done
sleep 0.6 # let one dispatch cycle record backoff log
dump_task_state "$T3S" "1c-std: setup-failed (standard task, 1m retry backoff)"
shoot_pane "$T3S" "1c-setup-std"
chmod 0755 /tmp/walk-item1/sandboxes /tmp/walk-item1/sandboxes/runs 2>/dev/null || chmod 0755 /tmp/walk-item1/sandboxes
# Close T3S so it doesn't take worker slot during 1c-dur
api POST "/api/tasks/$T3S/close" '{}' >/dev/null

# 1c-dur. Setup failure (injected) on durable task -> SuspendSetup (2m suspension hold)
T3D=$(api POST /api/tasks '{"title":"Item1c-dur: Run now -> setup suspension (durable)","body":"walkthrough","repo":"acme/widgets","admission":"durable"}' | jq -r .id)
api POST "/api/tasks/$T3D/defer" '{}' >/dev/null
chmod 0555 /tmp/walk-item1/sandboxes /tmp/walk-item1/sandboxes/runs 2>/dev/null || chmod 0555 /tmp/walk-item1/sandboxes
probe_ui click "$DAEMON" "$CODE" "$T3D" "Run now" >/dev/null
for _ in $(seq 50); do
	cnt=$(api GET "/api/tasks/$T3D" | jq '.attempts | length')
	fin=$(api GET "/api/tasks/$T3D" | jq -r '.attempts[0].finishedAt // ""')
	[[ "$cnt" -ge 1 && -n "$fin" ]] && break
	sleep 0.3
done
sleep 0.8 # let dispatch cycle record SuspensionWaitCapability ("held until <t> by its suspension")
dump_task_state "$T3D" "1c-dur: SuspendSetup (durable task, held until <t> by its suspension)"
grep -E "run-now request for ($T3S|$T3D) skipped" /tmp/walk-item1/daemon.log | tail -n 6 || true
shoot_pane "$T3D" "1c-setup-durable"
chmod 0755 /tmp/walk-item1/sandboxes /tmp/walk-item1/sandboxes/runs 2>/dev/null || chmod 0755 /tmp/walk-item1/sandboxes
echo "-- Pressing Run now AGAIN on stale kept request $T3D (verifying immediate re-stamp dispatch):"
t_repress=$(date +%s%3N)
probe_ui click "$DAEMON" "$CODE" "$T3D" "Run now" >/dev/null
for _ in $(seq 40); do
	cnt=$(api GET "/api/tasks/$T3D" | jq '.attempts | length')
	[[ "$cnt" -ge 2 ]] && break
	sleep 0.2
done
t_started2=$(date +%s%3N)
echo "Re-stamped Run now started Attempt #2 in $((t_started2 - t_repress)) ms!"
for _ in $(seq 30); do
	[[ $(api GET "/api/tasks/$T3D" | jq -r .state) != "running" ]] && break
	sleep 0.3
done

# 1d. Operator pause mid-run
T4=$(api POST /api/tasks '{"title":"Item1d: Run now -> operator pause","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
api POST "/api/tasks/$T4/defer" '{}' >/dev/null
cat > "/tmp/walk-item1/action-$T4.json" <<'EOF'
[{"kind":"sleep","seconds":2},{"kind":"nothing_needed","reason":"resumed after operator pause"}]
EOF
api POST "/api/tasks/$T4/run-now" '' >/dev/null
for _ in $(seq 50); do
	[[ -f "/tmp/walk-item1/live-$T4" ]] && break
	sleep 0.2
done
api POST "/api/tasks/$T4/pause" '{"reason":"walkthrough operator pause"}' >/dev/null
for _ in $(seq 40); do
	fin=$(api GET "/api/tasks/$T4" | jq -r '.attempts[0].finishedAt // ""')
	[[ -n "$fin" ]] && break
	sleep 0.2
done
sleep 0.6
dump_task_state "$T4" "1d: operator pause (while paused)"
shoot_pane "$T4" "1d-operator-pause"
echo "-- Resuming operator-paused task $T4 and measuring next start:"
t_res=$(date +%s%3N)
api POST "/api/tasks/$T4/resume" '{}' >/dev/null
for _ in $(seq 40); do
	cnt=$(api GET "/api/tasks/$T4" | jq '.attempts | length')
	[[ "$cnt" -ge 2 ]] && break
	sleep 0.2
done
t_res_start=$(date +%s%3N)
echo "Operator resume started Attempt #2 in $((t_res_start - t_res)) ms!"
for _ in $(seq 30); do
	[[ $(api GET "/api/tasks/$T4" | jq -r .state) != "running" ]] && break
	sleep 0.3
done

# 1e. Usage-limit stub
T5=$(api POST /api/tasks '{"title":"Item1e: Run now -> usage-limit stub","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
api POST "/api/tasks/$T5/defer" '{}' >/dev/null
cat > "/tmp/walk-item1/action-$T5.json" <<'EOF'
[{"kind":"usage_limit","message":"RESOURCE_EXHAUSTED: You have exhausted your capacity on this model. Your quota will reset after 30m0s."},{"kind":"nothing_needed","reason":"resumed after usage-limit lift"}]
EOF
api POST "/api/tasks/$T5/run-now" '' >/dev/null
for _ in $(seq 50); do
	cnt=$(api GET "/api/tasks/$T5" | jq '.attempts | length')
	fin=$(api GET "/api/tasks/$T5" | jq -r '.attempts[0].finishedAt // ""')
	[[ "$cnt" -ge 1 && -n "$fin" ]] && break
	sleep 0.3
done
sleep 0.8
dump_task_state "$T5" "1e: usage-limit stub (while connection paused)"
echo "-- GET /api/pause:"
api GET "/api/pause" | jq .
shoot_pane "$T5" "1e-usage-limit"
echo "-- Lifting usage-limit pause (DELETE /api/pause) and measuring next start:"
t_lift=$(date +%s%3N)
api DELETE "/api/pause" '' >/dev/null
for _ in $(seq 40); do
	cnt=$(api GET "/api/tasks/$T5" | jq '.attempts | length')
	[[ "$cnt" -ge 2 ]] && break
	sleep 0.2
done
t_lift_start=$(date +%s%3N)
echo "Lifting usage-limit pause started Attempt #2 in $((t_lift_start - t_lift)) ms!"
for _ in $(seq 30); do
	[[ $(api GET "/api/tasks/$T5" | jq -r .state) != "running" ]] && break
	sleep 0.3
done

# 1b. Deadline (both ResumeOnDeadline=true SuspendDeadline AND standard cancelled) using 3s-deadline daemon
POLL_INTERVAL=400ms start_daemon /tmp/grain-short-deadline /tmp/walk-item1

T2D=$(api POST /api/tasks '{"title":"Item1b-dur: Run now -> SuspendDeadline (resume_on_deadline)","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
sqlite3 "$DB" "UPDATE task SET resume_on_deadline = 1 WHERE id = '$T2D';"
api POST "/api/tasks/$T2D/defer" '{}' >/dev/null
cat > "/tmp/walk-item1/action-$T2D.json" <<'EOF'
[{"kind":"sleep","seconds":10},{"kind":"nothing_needed","reason":"resumed after SuspendDeadline"}]
EOF
api POST "/api/tasks/$T2D/run-now" '' >/dev/null
for _ in $(seq 50); do
	fin=$(api GET "/api/tasks/$T2D" | jq -r '.attempts[0].finishedAt // ""')
	[[ -n "$fin" ]] && break
	sleep 0.3
done
dump_task_state "$T2D" "1b-dur: SuspendDeadline (immediately after Attempt #1)"
# Wait for Attempt #2 (automatic deadline resume) to complete as well so we see when next run started!
for _ in $(seq 50); do
	cnt=$(api GET "/api/tasks/$T2D" | jq '.attempts | length')
	st=$(api GET "/api/tasks/$T2D" | jq -r .state)
	[[ "$cnt" -ge 2 && "$st" != "running" ]] && break
	sleep 0.3
done
dump_task_state "$T2D" "1b-dur: SuspendDeadline (after automatic Attempt #2)"

T2S=$(api POST /api/tasks '{"title":"Item1b-std: Run now -> deadline (standard)","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
api POST "/api/tasks/$T2S/defer" '{}' >/dev/null
cat > "/tmp/walk-item1/action-$T2S.json" <<'EOF'
[{"kind":"sleep","seconds":10},{"kind":"nothing_needed","reason":"done after retry backoff"}]
EOF
api POST "/api/tasks/$T2S/run-now" '' >/dev/null
for _ in $(seq 50); do
	fin=$(api GET "/api/tasks/$T2S" | jq -r '.attempts[0].finishedAt // ""')
	[[ -n "$fin" ]] && break
	sleep 0.3
done
sleep 0.6
dump_task_state "$T2S" "1b-std: deadline wall-clock cap (standard task)"
shoot_pane "$T2S" "1b-deadline-std"

stop_env /tmp/walk-item1

echo "================================================================"
echo "PART B: Upgrade store from c08832a2 with stale stamp -> HEAD Init"
echo "================================================================"

GRAIN=/tmp/grain-c08832a2 init_env /tmp/walk-upgrade
start_ghsim /tmp/walk-upgrade
POLL_INTERVAL=400ms start_daemon /tmp/grain-c08832a2 /tmp/walk-upgrade

UP_PARKED=$(api POST /api/tasks '{"title":"Upgrade: Run now -> resume_later on c08832a2","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
api POST "/api/tasks/$UP_PARKED/defer" '{}' >/dev/null
cat > "/tmp/walk-upgrade/action-$UP_PARKED.json" <<'EOF'
[{"kind":"resume_later","mcp_args":{"reason":"c08832a2 park 1h","when":{"at":"1h"}}},{"kind":"nothing_needed","reason":"dispatched after HEAD upgrade"}]
EOF
api POST "/api/tasks/$UP_PARKED/run-now" '' >/dev/null
for _ in $(seq 50); do
	st=$(api GET "/api/tasks/$UP_PARKED" | jq -r '.state + ":" + (.attempts | length | tostring)')
	[[ "$st" == "deferred:1" ]] && break
	sleep 0.3
done

echo "-- c08832a2 task $UP_PARKED right after resume_later:"
sqlite3 -json "$DB" "SELECT o.task_id, o.run_now_at, t.deferred_until FROM task_observation o JOIN task t ON t.id = o.task_id WHERE o.task_id = '$UP_PARKED';"
api GET "/api/tasks/$UP_PARKED" | jq '{id, state, runNowAt, comments: [.comments[] | {author, body}]}'

# Also create a closed task holding a stale stamp, and a setup-failed task holding a genuine kept stamp
UP_CLOSED=$(api POST /api/tasks '{"title":"Upgrade: closed with stale stamp","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
api POST "/api/tasks/$UP_CLOSED/defer" '{}' >/dev/null
# Pause pipeline or close immediately after stamping on c08832a2:
api POST "/api/tasks/$UP_CLOSED/pause" '{"reason":"hold"}' >/dev/null
api POST "/api/tasks/$UP_CLOSED/run-now" '' >/dev/null
api POST "/api/tasks/$UP_CLOSED/close" '{}' >/dev/null

UP_KEPT=$(api POST /api/tasks '{"title":"Upgrade: setup-failed kept stamp","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
api POST "/api/tasks/$UP_KEPT/defer" '{}' >/dev/null
chmod 0555 /tmp/walk-upgrade/sandboxes /tmp/walk-upgrade/sandboxes/runs 2>/dev/null || chmod 0555 /tmp/walk-upgrade/sandboxes
api POST "/api/tasks/$UP_KEPT/run-now" '' >/dev/null
for _ in $(seq 50); do
	fin=$(api GET "/api/tasks/$UP_KEPT" | jq -r '.attempts[0].finishedAt // ""')
	[[ -n "$fin" ]] && break
	sleep 0.3
done
chmod 0755 /tmp/walk-upgrade/sandboxes /tmp/walk-upgrade/sandboxes/runs 2>/dev/null || chmod 0755 /tmp/walk-upgrade/sandboxes

# Now undefer UP_PARKED on c08832a2 and show c08832a2 holds it stuck!
api POST "/api/tasks/$UP_PARKED/undefer" '{}' >/dev/null
sleep 2.0
echo "-- c08832a2 BEFORE upgrade (2s after undefer — still stuck at 1 attempt!):"
sqlite3 -json "$DB" "SELECT task_id, run_now_at, closed_at FROM task_observation WHERE task_id IN ('$UP_PARKED','$UP_CLOSED','$UP_KEPT') ORDER BY CAST(task_id AS INTEGER);"
api GET "/api/tasks/$UP_PARKED" | jq '{id, state, runNowAt, attemptCount: (.attempts | length)}'
grep -E "backing off|run-now" /tmp/walk-upgrade/daemon.log | tail -n 5 || true

# Upgrade to HEAD!
echo "-- Upgrading daemon to HEAD..."
POLL_INTERVAL=400ms start_daemon /home/debian/bin/grain /tmp/walk-upgrade
echo "-- Immediately after HEAD Store.Init:"
sqlite3 -json "$DB" "SELECT task_id, run_now_at, closed_at FROM task_observation WHERE task_id IN ('$UP_PARKED','$UP_CLOSED','$UP_KEPT') ORDER BY CAST(task_id AS INTEGER);"

for _ in $(seq 40); do
	cnt=$(api GET "/api/tasks/$UP_PARKED" | jq '.attempts | length')
	st=$(api GET "/api/tasks/$UP_PARKED" | jq -r .state)
	[[ "$cnt" -ge 2 && "$st" != "running" ]] && break
	sleep 0.2
done
echo "-- HEAD task $UP_PARKED released and completed Attempt #2:"
api GET "/api/tasks/$UP_PARKED" | jq '{id, state, runNowAt, runNowStale, attempts: [.attempts[] | {attempt, outcome, suspendKind}]}'

stop_env /tmp/walk-upgrade
echo "ITEM 1 COMPLETE!"
