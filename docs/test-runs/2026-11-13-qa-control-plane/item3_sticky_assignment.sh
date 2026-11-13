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

# Item 3 (§2): Sticky work-tree arm assignment over 50 tasks in shared-origin
# groups at 30% rollout, treatment share check, retry stickiness across rollout
# changes within an epoch, and re-randomization on epoch bump.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

work="/tmp/walk-qa-item3-${STORE:-sqlite}"
trap 'stop_env "$work"' EXIT

MAX_WORKERS=4 init_env "$work" "$GRAIN"
start_ghsim "$work"
MAX_WORKERS=4 start_daemon "$GRAIN" "$work"
sleep 1.2

qa_cli() {
	"$GRAIN" -server "$DAEMON" -token "$CODE" "$@"
}

echo "=== [Item 3 / store=${STORE:-sqlite}] ==="

# 1. Enable QA at 30% rollout, epoch 1
qa_cli qa enable -flow golden-v1-minimal -rollout 30 -epoch 1 >/dev/null
echo "  initial QA status: $(qa_cli --json qa status | jq -c .)"

# 2. File 50 tasks across 20 distinct work-tree root origins:
#    - 10 GitHub PR/issue origins (acme/widgets#201 .. acme/widgets#210), 3 tasks each = 30 tasks
#    - 10 grain task-tree origins (root task + child task naming `origin: "task <root>"`), 2 tasks each = 20 tasks
declare -a ALL_TIDS=()
declare -A EXPECTED_ORIGIN=()

for g in $(seq 201 210); do
	origin="acme/widgets#${g}"
	for k in 1 2 3; do
		resp=$(api POST /api/tasks "{\"title\":\"GH group $g member $k\",\"body\":\"Task $k in work tree $origin\",\"repo\":\"acme/widgets\",\"origin\":\"$origin\"}")
		tid=$(echo "$resp" | jq -r '.id')
		ALL_TIDS+=("$tid")
		EXPECTED_ORIGIN["$tid"]="$origin"
	done
done

for t in $(seq 1 10); do
	resp_root=$(api POST /api/tasks "{\"title\":\"Tree root $t\",\"body\":\"Root task for work tree $t with schema migration notes\",\"repo\":\"acme/widgets\"}")
	rtid=$(echo "$resp_root" | jq -r '.id')
	origin="task:${rtid}"
	ALL_TIDS+=("$rtid")
	EXPECTED_ORIGIN["$rtid"]="$origin"

	resp_child=$(api POST /api/tasks "{\"title\":\"Tree child $t\",\"body\":\"Follow-up child in work tree $origin\",\"repo\":\"acme/widgets\",\"origin\":\"task $rtid\"}")
	ctid=$(echo "$resp_child" | jq -r '.id')
	ALL_TIDS+=("$ctid")
	EXPECTED_ORIGIN["$ctid"]="$origin"
done

echo "  filed ${#ALL_TIDS[@]} tasks across 20 root origins; approving all..."
for tid in "${ALL_TIDS[@]}"; do
	api POST "/api/tasks/${tid}/approve" '{}' >/dev/null
done

for tid in "${ALL_TIDS[@]}"; do
	wait_task_closed "$tid" 250
done

# 3. Analyze assignments from perf/qa-checks.jsonl and task_qa_assignment
PERF_FILE="$work/data/perf/qa-checks.jsonl"
if [[ ! -s "$PERF_FILE" ]]; then
	echo "FAIL: expected non-empty $PERF_FILE" >&2
	exit 1
fi

python3 - "$PERF_FILE" <<'PY'
import json, sys, collections

perf_path = sys.argv[1]
runs_by_task = collections.defaultdict(list)
with open(perf_path, "r", encoding="utf-8") as f:
    for line in f:
        row = json.loads(line)
        if row.get("stage") == "S0" and row.get("checkId") == "arm-assign":
            runs_by_task[row["taskId"]].append(row["verdict"])

print(f"  perf JSONL S0 arm-assign samples: {sum(len(v) for v in runs_by_task.values())} across {len(runs_by_task)} tasks")
if len(runs_by_task) < 50:
    raise SystemExit(f"FAIL: expected >=50 tasks in perf JSONL, got {len(runs_by_task)}")
PY

# Verify via SQL that task_qa_assignment has exactly 20 rows for epoch=1 and every task in each group got its group's arm.
assign_rows_e1=$(sql_scalar "SELECT COUNT(*) FROM task_qa_assignment WHERE epoch = 1;")
treat_rows_e1=$(sql_scalar "SELECT COUNT(*) FROM task_qa_assignment WHERE epoch = 1 AND arm = 'treatment';")
ctrl_rows_e1=$(sql_scalar "SELECT COUNT(*) FROM task_qa_assignment WHERE epoch = 1 AND arm = 'control';")
echo "  epoch=1 distinct root_origin rows: total=$assign_rows_e1 treatment=$treat_rows_e1 control=$ctrl_rows_e1"
if [[ "$assign_rows_e1" -ne 20 ]]; then
	echo "FAIL: expected 20 sticky root_origin rows in epoch 1, got $assign_rows_e1" >&2
	exit 1
fi
# Binomial tolerance for n=20, p=0.30: expected 6, wide non-flaky bounds [2, 12] (deterministic sha256 anyway!)
if [[ "$treat_rows_e1" -lt 2 || "$treat_rows_e1" -gt 12 ]]; then
	echo "FAIL: treatment count $treat_rows_e1 out of tolerance [2, 12] for 20 origins at 30%" >&2
	exit 1
fi

# Verify every member task's recorded S0 verdict matches its group's stored arm in task_qa_assignment!
mismatches=0
for tid in "${ALL_TIDS[@]}"; do
	orig="${EXPECTED_ORIGIN[$tid]}"
	stored_arm=$(sql_scalar "SELECT arm FROM task_qa_assignment WHERE root_origin = '$orig' AND epoch = 1;")
	perf_arm=$(jq -r --arg tid "$tid" 'select(.stage=="S0" and .checkId=="arm-assign" and .taskId==$tid) | .verdict' "$PERF_FILE" | head -n1)
	if [[ -z "$stored_arm" || "$stored_arm" != "$perf_arm" ]]; then
		echo "  MISMATCH task=$tid origin=$orig stored=$stored_arm perf=$perf_arm" >&2
		mismatches=$((mismatches + 1))
	fi
done
echo "  per-task vs root-origin arm mismatches across 50 tasks: $mismatches"
if [[ "$mismatches" -ne 0 ]]; then
	exit 1
fi

# 4. Retry / second-attempt stickiness: attempt 1 finishes and records arm in epoch 1;
#    rollout changes to opposite extreme (0% or 100%) in the same epoch 1;
#    reopening/re-running dispatches attempt 2, which must keep attempt 1's arm!
resp_retry=$(api POST /api/tasks '{"title":"Retry stickiness probe","body":"Verify arm sticks across task retry even after rolloutPct changes","repo":"acme/widgets","origin":"acme/widgets#999"}')
rt_id=$(echo "$resp_retry" | jq -r '.id')
api POST "/api/tasks/${rt_id}/approve" '{}' >/dev/null
wait_task_closed "$rt_id" 150

first_arm=$(sql_scalar "SELECT arm FROM task_qa_assignment WHERE root_origin = 'acme/widgets#999' AND epoch = 1;")
if [[ "$first_arm" == "treatment" ]]; then
	opposite_pct=0
else
	opposite_pct=100
fi
qa_cli qa enable -rollout "$opposite_pct" >/dev/null
sleep 0.7

api POST "/api/tasks/${rt_id}/reopen" '{}' >/dev/null
api POST "/api/tasks/${rt_id}/comments" '{"body":"re-run to verify sticky arm under opposite rolloutPct"}' >/dev/null
for _ in $(seq 150); do
	runs_done=$(sql_scalar "SELECT COUNT(*) FROM task_run WHERE task_id = '$rt_id' AND finished_at IS NOT NULL;")
	if [[ "$runs_done" -ge 2 ]]; then
		break
	fi
	sleep 0.2
done
wait_task_closed "$rt_id" 150

distinct_retry_arms=$(jq -r --arg tid "$rt_id" 'select(.stage=="S0" and .checkId=="arm-assign" and .taskId==$tid) | .verdict' "$PERF_FILE" | sort -u | tr '\n' ' ')
retry_run_count=$(jq -r --arg tid "$rt_id" 'select(.stage=="S0" and .checkId=="arm-assign" and .taskId==$tid) | .verdict' "$PERF_FILE" | wc -l)
echo "  retry stickiness (task $rt_id, opposite rollout=${opposite_pct}%): runs=$retry_run_count distinct_arms=[$distinct_retry_arms] (initial=$first_arm)"
if [[ "$(echo "$distinct_retry_arms" | xargs)" != "$first_arm" || "$retry_run_count" -lt 2 ]]; then
	echo "FAIL: retry did not preserve sticky arm '$first_arm' across >=2 runs (got '$distinct_retry_arms' over $retry_run_count runs)" >&2
	exit 1
fi

# 5. Bump epoch -> 2 at 30% rollout and dispatch across acme/widgets#201..210: assignments re-randomize!
qa_cli qa enable -rollout 30 -epoch 2 >/dev/null
declare -a EPOCH2_TIDS=()
for g in $(seq 201 210); do
	origin="acme/widgets#${g}"
	resp=$(api POST /api/tasks "{\"title\":\"Epoch 2 check $g\",\"body\":\"Re-randomized epoch 2 check for $origin\",\"repo\":\"acme/widgets\",\"origin\":\"$origin\"}")
	tid=$(echo "$resp" | jq -r '.id')
	EPOCH2_TIDS+=("$tid")
	api POST "/api/tasks/${tid}/approve" '{}' >/dev/null
done
for tid in "${EPOCH2_TIDS[@]}"; do
	wait_task_closed "$tid" 200
done

flipped=$(sql_scalar "SELECT COUNT(*) FROM task_qa_assignment a1 JOIN task_qa_assignment a2 ON a1.root_origin = a2.root_origin WHERE a1.epoch = 1 AND a2.epoch = 2 AND a1.arm <> a2.arm;")
e2_count=$(sql_scalar "SELECT COUNT(*) FROM task_qa_assignment WHERE epoch = 2;")
echo "  epoch=2 rows=$e2_count, origins that flipped arm between epoch 1 and epoch 2: $flipped / 10"
if [[ "$e2_count" -ne 10 || "$flipped" -lt 1 ]]; then
	echo "FAIL: expected 10 epoch=2 rows and >=1 flipped origin, got rows=$e2_count flipped=$flipped" >&2
	exit 1
fi
