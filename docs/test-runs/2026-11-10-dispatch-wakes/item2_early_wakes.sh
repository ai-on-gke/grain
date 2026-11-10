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

# Executes §9 Item 2: Early wakes from resume_later ({at: 2h, message: true},
# {at: 2h, ci: my-branch}, {at: 2h, task: <id>} + {task: <id>}, UI Undefer),
# console session & goal task "woken, waiting for dispatch" preservation,
# and setup suspension hold with "held until <t> by its suspension".

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=docs/test-runs/2026-11-10-dispatch-wakes/env.sh
source "$HERE/env.sh"

cleanup() {
	chmod 0755 /tmp/walk-item2/sandboxes /tmp/walk-item2/sandboxes/runs 2>/dev/null || true
	stop_env /tmp/walk-item2 2>/dev/null || true
}
trap cleanup EXIT

probe_ui() {
	PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node "$HERE/ui_probe.mjs" "$@"
}

init_env /tmp/walk-item2
start_ghsim /tmp/walk-item2
POLL_INTERVAL=500ms start_daemon /home/debian/bin/grain /tmp/walk-item2

# Put acme/widgets on ci_mode: github so {ci: "my-branch"} exercises GitHub Actions checks!
api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"github"}' >/dev/null

echo "================================================================"
echo "2a. resume_later {at: 2h, message: true} woken early by message"
echo "================================================================"
T_MSG=$(api POST /api/tasks '{"title":"Item2a: {at: 2h, message: true}","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
cat > "/tmp/walk-item2/action-$T_MSG.json" <<'EOF'
[{"kind":"resume_later","mcp_args":{"reason":"park 2h or message","when":{"at":"2h","message":true}}},{"kind":"nothing_needed","reason":"woken by message"}]
EOF
api POST "/api/tasks/$T_MSG/approve" '{}' >/dev/null
for _ in $(seq 40); do
	[[ $(api GET "/api/tasks/$T_MSG" | jq -r '.state + ":" + (.attempts|length|tostring)') == "deferred:1" ]] && break
	sleep 0.2
done
echo "-- Parked state before message:"
sqlite3 -json "$DB" "SELECT task_id, kind, resume_on, resume_at, resumed_at FROM task_suspension WHERE task_id = '$T_MSG';"
t0=$(date +%s%3N)
api POST "/api/tasks/$T_MSG/comments" '{"body":"early message wake!"}' >/dev/null
for _ in $(seq 40); do
	[[ $(api GET "/api/tasks/$T_MSG" | jq '.attempts | length') -ge 2 ]] && break
	sleep 0.1
done
t1=$(date +%s%3N)
echo "Message wake -> Attempt #2 started in $((t1 - t0)) ms (wall clock)!"
for _ in $(seq 30); do [[ $(api GET "/api/tasks/$T_MSG" | jq -r .state) != "running" ]] && break; sleep 0.2; done
api GET "/api/tasks/$T_MSG" | jq '{id, state, attempts: [.attempts[] | {startedAt, finishedAt, outcome}]}'
sqlite3 -json "$DB" "SELECT * FROM task_suspension WHERE task_id = '$T_MSG';"

echo "================================================================"
echo "2b. resume_later {at: 2h, ci: my-branch} woken early by CI verdict"
echo "================================================================"
T_CI=$(api POST /api/tasks '{"title":"Item2b: {at: 2h, ci: my-branch}","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
python3 - "$T_CI" "$GHSIM" <<'PY'
import json, sys
tid, ghsim = sys.argv[1], sys.argv[2]
cmd = (
    "cd work && branch=$(git symbolic-ref --short HEAD) && echo ci-test > ci.txt && "
    "git add ci.txt && git commit -m 'chore: test ci wake' -m 'Body.' && "
    "git push origin HEAD:$branch && sha=$(git rev-parse HEAD) && "
    f"curl -sS -X POST http://{ghsim}/_sim/check-runs "
    "-d \"{\\\"ref\\\":\\\"$sha\\\",\\\"checks\\\":[{\\\"id\\\":501,\\\"name\\\":\\\"Tests / go\\\",\\\"status\\\":\\\"in_progress\\\"}]}\""
)
actions = [
    {
        "kind": "mcp_sequence",
        "calls": [
            {"tool": "run_command", "args": {"command": cmd, "description": "push branch and seed in-progress Actions check"}},
            {"tool": "resume_later", "args": {"reason": "park 2h or CI verdict", "when": {"at": "2h", "ci": "my-branch"}}},
        ],
    },
    {"kind": "nothing_needed", "reason": "woken by CI verdict"},
]
open(f"/tmp/walk-item2/action-{tid}.json", "w").write(json.dumps(actions))
PY
api POST "/api/tasks/$T_CI/approve" '{}' >/dev/null
for _ in $(seq 60); do
	[[ $(api GET "/api/tasks/$T_CI" | jq -r '.state + ":" + (.attempts|length|tostring)') == "deferred:1" ]] && break
	sleep 0.25
done
echo "-- Parked on {at: 2h, ci: my-branch}:"
sqlite3 -json "$DB" "SELECT task_id, kind, resume_on, resume_at, resumed_at FROM task_suspension WHERE task_id = '$T_CI';"
HEAD_SHA=$(git -C /tmp/walk-item2/widgets.git for-each-ref --format='%(objectname) %(refname:short)' refs/heads/grain/ | awk '{print $1}' | head -n1)
echo "Completing check-run on SHA $HEAD_SHA..."
t0=$(date +%s%3N)
curl -sS -X POST "http://$GHSIM/_sim/check-runs" -d "{\"ref\":\"$HEAD_SHA\",\"checks\":[{\"id\":501,\"name\":\"Tests / go\",\"status\":\"completed\",\"conclusion\":\"success\"}]}" >/dev/null
for _ in $(seq 60); do
	[[ $(api GET "/api/tasks/$T_CI" | jq '.attempts | length') -ge 2 ]] && break
	sleep 0.1
done
t1=$(date +%s%3N)
echo "CI verdict -> Attempt #2 started in $((t1 - t0)) ms (wall clock)!"
for _ in $(seq 30); do [[ $(api GET "/api/tasks/$T_CI" | jq -r .state) != "running" ]] && break; sleep 0.2; done
api GET "/api/tasks/$T_CI" | jq '{id, state, attempts: [.attempts[] | {startedAt, finishedAt, outcome}]}'

echo "================================================================"
echo "2c. resume_later {at: 2h, task: <id>} refusal + {task: <id>} early wake"
echo "================================================================"
T_DEP=$(api POST /api/tasks '{"title":"Item2c blocker task","body":"blocker","repo":"acme/widgets"}' | jq -r .id)
# Leave T_DEP unapproved so it stays open without running
T_WAIT=$(api POST /api/tasks '{"title":"Item2c waiter task","body":"waiter","repo":"acme/widgets"}' | jq -r .id)
cat > "/tmp/walk-item2/action-$T_WAIT.json" <<EOF
[
  {
    "kind": "mcp_sequence",
    "calls": [
      {
        "tool": "resume_later",
        "args": {"reason": "mixed task+at", "when": {"at": "2h", "task": "$T_DEP"}}
      },
      {
        "tool": "resume_later",
        "args": {"reason": "wait on blocker task only", "when": {"task": "$T_DEP"}}
      }
    ]
  },
  {"kind": "nothing_needed", "reason": "woken by blocker task closing"}
]
EOF
api POST "/api/tasks/$T_WAIT/approve" '{}' >/dev/null
for _ in $(seq 40); do
	[[ $(api GET "/api/tasks/$T_WAIT" | jq -r '.state + ":" + (.attempts|length|tostring)') == "queued:1" ]] && break
	sleep 0.2
done
echo "-- MCP tool outputs (recording refusal of {at: 2h, task: <id>} followed by {task: <id>}):"
cat "/tmp/walk-item2/runs/mcp-$T_WAIT.jsonl"
sqlite3 -json "$DB" "SELECT task_id, kind, resume_on, resume_at, resumed_at FROM task_suspension WHERE task_id = '$T_WAIT';"
t0=$(date +%s%3N)
api POST "/api/tasks/$T_DEP/close" '{}' >/dev/null
for _ in $(seq 40); do
	[[ $(api GET "/api/tasks/$T_WAIT" | jq '.attempts | length') -ge 2 ]] && break
	sleep 0.1
done
t1=$(date +%s%3N)
echo "Closing dependency task $T_DEP -> Attempt #2 started in $((t1 - t0)) ms (wall clock)!"
for _ in $(seq 30); do [[ $(api GET "/api/tasks/$T_WAIT" | jq -r .state) != "running" ]] && break; sleep 0.2; done

echo "================================================================"
echo "2d. resume_later {at: 2h} woken early via Undefer button in UI"
echo "================================================================"
T_UND=$(api POST /api/tasks '{"title":"Item2d: {at: 2h} -> UI Undefer","body":"walkthrough","repo":"acme/widgets"}' | jq -r .id)
cat > "/tmp/walk-item2/action-$T_UND.json" <<'EOF'
[{"kind":"resume_later","mcp_args":{"reason":"park 2h until UI Undefer","when":{"at":"2h"}}},{"kind":"nothing_needed","reason":"woken by UI Undefer"}]
EOF
api POST "/api/tasks/$T_UND/approve" '{}' >/dev/null
for _ in $(seq 40); do
	[[ $(api GET "/api/tasks/$T_UND" | jq -r '.state + ":" + (.attempts|length|tostring)') == "deferred:1" ]] && break
	sleep 0.2
done
t0=$(date +%s%3N)
probe_ui click "$DAEMON" "$CODE" "$T_UND" "Undefer"
t_clicked=$(date +%s%3N)
for _ in $(seq 40); do
	[[ $(api GET "/api/tasks/$T_UND" | jq '.attempts | length') -ge 2 ]] && break
	sleep 0.1
done
t1=$(date +%s%3N)
echo "UI Undefer click finished at +$((t_clicked - t0)) ms -> Attempt #2 started $((t1 - t_clicked)) ms after click!"
for _ in $(seq 30); do [[ $(api GET "/api/tasks/$T_UND" | jq -r .state) != "running" ]] && break; sleep 0.2; done

echo "================================================================"
echo "2e. Console session & goal task remain woken, waiting for dispatch"
echo "================================================================"
# Create a clean console session so it runs Turn 1 and rests on {message: true}!
api POST /api/console-sessions '{"id":"ops","title":"Ops Session","body":"initial ops check"}' >/dev/null
T_CS=""
for _ in $(seq 30); do
	T_CS=$(sqlite3 "$DB" "SELECT task_id FROM task_tag WHERE tag = 'console-session:ops' LIMIT 1;")
	[[ -n "$T_CS" ]] && break
	sleep 0.2
done
cat > "/tmp/walk-item2/action-$T_CS.json" <<'EOF'
[{"kind":"plain_reply","response":"Ops turn 1 complete."},{"kind":"plain_reply","response":"Ops turn 2 complete."}]
EOF
api POST "/api/tasks/$T_CS/comments" '{"body":"start ops turn 1"}' >/dev/null
for _ in $(seq 50); do
	[[ $(api GET "/api/tasks/$T_CS" | jq -r '.state + ":" + (.attempts|length|tostring)') == "deferred:1" ]] && break
	sleep 0.25
done
echo "-- Ops console session (Task $T_CS) resting after Turn 1:"
sqlite3 -json "$DB" "SELECT task_id, kind, resume_on, resumed_at FROM task_suspension WHERE task_id = '$T_CS';"

# Goal task: file a task with goal person-closes, run Turn 1 so restOnGoal parks it on {goal: true}.
T_GOAL=$(api POST /api/tasks '{"title":"Item2e: Goal task person-closes","body":"goal walk","repo":"acme/widgets"}' | jq -r .id)
api PUT "/api/tasks/$T_GOAL/goal" '{"kind":"person-closes"}' >/dev/null
cat > "/tmp/walk-item2/action-$T_GOAL.json" <<'EOF'
[{"kind":"comment_on_issue","comment":"goal turn 1 note"},{"kind":"comment_on_issue","comment":"goal turn 2 note"}]
EOF
api POST "/api/tasks/$T_GOAL/approve" '{}' >/dev/null
for _ in $(seq 50); do
	[[ $(api GET "/api/tasks/$T_GOAL" | jq -r '.state + ":" + (.attempts|length|tostring)') == "deferred:1" ]] && break
	sleep 0.25
done
echo "-- Goal task ($T_GOAL) resting after Turn 1:"
sqlite3 -json "$DB" "SELECT task_id, kind, resume_on, resumed_at FROM task_suspension WHERE task_id = '$T_GOAL';"

# Now pause the pipeline so dispatch waits, wake BOTH T_CS and T_GOAL, and verify across 4 reconcile cycles (~2s)
# that NEITHER reconcileConsoleSessions NOR reconcileGoals re-defers them while their open SuspendRequested suspension stands!
api POST /api/pipeline-pause '{"reason":"hold dispatch to observe woken state"}' >/dev/null
api POST "/api/tasks/$T_CS/comments" '{"body":"wake console session while pipeline held"}' >/dev/null
api POST "/api/tasks/$T_GOAL/comments" '{"body":"wake goal task while pipeline held"}' >/dev/null
sleep 2.0
echo "-- After 4 reconcile cycles while waiting for dispatch (both must stay queued, deferredAt=null, suspension open):"
api GET "/api/tasks/$T_CS" | jq '{id, state, deferredAt, deferredUntil, reason}'
api GET "/api/tasks/$T_GOAL" | jq '{id, state, deferredAt, deferredUntil, reason, goal: {deferredByGoal: .goal.deferredByGoal}}'
sqlite3 -json "$DB" "SELECT task_id, kind, resume_on, resumed_at FROM task_suspension WHERE task_id IN ('$T_CS','$T_GOAL') ORDER BY CAST(task_id AS INTEGER);"

# Resume pipeline and confirm both dispatch immediately!
api DELETE /api/pipeline-pause '' >/dev/null
for _ in $(seq 50); do
	c1=$(api GET "/api/tasks/$T_CS" | jq '.attempts | length')
	cg=$(api GET "/api/tasks/$T_GOAL" | jq '.attempts | length')
	[[ "$c1" -ge 2 && "$cg" -ge 2 ]] && break
	sleep 0.2
done
echo "-- Both console session ($T_CS) and goal task ($T_GOAL) dispatched Attempt #2 once pipeline unpaused!"
# Close goal task so it stops resting
for _ in $(seq 30); do [[ $(api GET "/api/tasks/$T_GOAL" | jq -r .state) != "running" ]] && break; sleep 0.2; done
api POST "/api/tasks/$T_GOAL/close" '{}' >/dev/null

echo "================================================================"
echo "2f. Setup suspension still holds with 'held until <t> by its suspension'"
echo "================================================================"
T_HOLD=$(api POST /api/tasks '{"title":"Item2f: Durable setup hold","body":"walkthrough","repo":"acme/widgets","admission":"durable"}' | jq -r .id)
chmod 0555 /tmp/walk-item2/sandboxes /tmp/walk-item2/sandboxes/runs 2>/dev/null || chmod 0555 /tmp/walk-item2/sandboxes
api POST "/api/tasks/$T_HOLD/approve" '{}' >/dev/null
for _ in $(seq 40); do
	fin=$(api GET "/api/tasks/$T_HOLD" | jq -r '.attempts[0].finishedAt // ""')
	[[ -n "$fin" ]] && break
	sleep 0.2
done
chmod 0755 /tmp/walk-item2/sandboxes /tmp/walk-item2/sandboxes/runs 2>/dev/null || chmod 0755 /tmp/walk-item2/sandboxes
sleep 1.5
echo "-- Task $T_HOLD state after 3 cycles (must still hold with queued/held_by_suspension and 1 attempt):"
api GET "/api/tasks/$T_HOLD" | jq '{id, state, attemptCount: (.attempts|length), activity, activityBySetup, reason}'
echo "-- Daemon log lines for setup suspension hold:"
grep "candidate $T_HOLD skipped" /tmp/walk-item2/daemon.log | tail -n 5

stop_env /tmp/walk-item2
echo "ITEM 2 COMPLETE!"
