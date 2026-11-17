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

# Item 1 (§2): Prove that QA off really is off on SQLite and PostgreSQL, and
# compare per-dispatch store writes against baseline commit 24d0eba1 (schema 139).

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

BASELINE_BIN=${BASELINE_BIN:-/home/debian/bin/grain-24d0eba1}
HEAD_BIN=${HEAD_BIN:-/home/debian/bin/grain}

measure_table_counts() {
	local task_c run_c cmt_c lnk_c obs_c
	task_c=$(sql_scalar "SELECT COUNT(*) FROM task;")
	run_c=$(sql_scalar "SELECT COUNT(*) FROM task_run;")
	cmt_c=$(sql_scalar "SELECT COUNT(*) FROM task_comment;")
	lnk_c=$(sql_scalar "SELECT COUNT(*) FROM task_link;")
	obs_c=$(sql_scalar "SELECT COUNT(*) FROM task_observation;")
	echo "task=${task_c},task_run=${run_c},task_comment=${cmt_c},task_link=${lnk_c},task_observation=${obs_c}"
}

measure_pg_tuples() {
	if [[ ${STORE:-sqlite} == postgres ]]; then
		sql_scalar "SELECT COALESCE(SUM(n_tup_ins),0) || ' ins / ' || COALESCE(SUM(n_tup_upd),0) || ' upd / ' || COALESCE(SUM(n_tup_del),0) || ' del' FROM pg_stat_user_tables WHERE relname IN ('task','task_run','task_comment','task_link','task_observation');"
	else
		echo "n/a (sqlite)"
	fi
}

run_five_dispatches() (
	local label=$1
	local bin=$2
	local work="/tmp/walk-qa-item1-${STORE:-sqlite}-${label}"
	trap 'stop_env "$work"' EXIT

	POLL_INTERVAL=500ms MAX_WORKERS=1 init_env "$work" "$bin"
	start_ghsim "$work"
	POLL_INTERVAL=500ms MAX_WORKERS=1 start_daemon "$bin" "$work"

	# Wait until seed-models.yaml has been picked up and any initial session/sync settles.
	sleep 1.5

	local counts_before pg_before
	counts_before=$(measure_table_counts)
	pg_before=$(measure_pg_tuples)

	for i in 1 2 3 4 5; do
		local resp tid
		resp=$(api POST /api/tasks "{\"title\":\"Off check task $i\",\"body\":\"Verify zero QA overhead on dispatch $i\",\"repo\":\"acme/widgets\",\"approve\":false}")
		tid=$(echo "$resp" | jq -r '.id')
		if [[ "$i" -ge 4 ]]; then
			cat >"$work/action-${tid}.json" <<'EOF'
{"kind": "push_pr"}
EOF
		fi
		api POST "/api/tasks/${tid}/approve" '{}' >/dev/null

		for _ in $(seq 150); do
			local finished
			finished=$(sql_scalar "SELECT COUNT(*) FROM task_run WHERE task_id = '$tid' AND finished_at IS NOT NULL;")
			if [[ "$finished" -ge 1 ]]; then
				break
			fi
			sleep 0.2
		done
		local finished_final
		finished_final=$(sql_scalar "SELECT COUNT(*) FROM task_run WHERE task_id = '$tid' AND finished_at IS NOT NULL;")
		if [[ "$finished_final" -lt 1 ]]; then
			echo "ERROR: [$label/${STORE:-sqlite}] task $tid never finished its run" >&2
			tail -n 30 "$work/daemon.log" >&2
			exit 1
		fi
	done

	sleep 0.6
	local counts_after pg_after metrics_commits
	counts_after=$(measure_table_counts)
	pg_after=$(measure_pg_tuples)
	metrics_commits=$(api GET "/api/metrics?window=1h" | jq -c '.cycles.commits // {}')

	echo "=== [$label / store=${STORE:-sqlite}] ==="
	echo "  rows before:     $counts_before"
	echo "  rows after:      $counts_after"
	echo "  pg tuples pre:   $pg_before"
	echo "  pg tuples post:  $pg_after"
	echo "  cycles.commits:  $metrics_commits"

	if [[ "$label" == "head" ]]; then
		local qa_cfg assign_c check_c find_c
		qa_cfg=$(sql_scalar "SELECT qa FROM grain_config WHERE id = 1;")
		assign_c=$(sql_scalar "SELECT COUNT(*) FROM task_qa_assignment;")
		check_c=$(sql_scalar "SELECT COUNT(*) FROM task_qa_check_run;")
		find_c=$(sql_scalar "SELECT COUNT(*) FROM task_qa_finding;")
		echo "  grain_config.qa: '$qa_cfg'"
		echo "  task_qa_* rows:  assignment=$assign_c check_run=$check_c finding=$find_c"

		if [[ -n "$qa_cfg" ]]; then
			echo "FAIL: expected empty grain_config.qa on untouched deployment, got '$qa_cfg'" >&2
			exit 1
		fi
		if [[ "$assign_c" != "0" || "$check_c" != "0" || "$find_c" != "0" ]]; then
			echo "FAIL: expected 0 rows in all task_qa_* tables while QA is off" >&2
			exit 1
		fi
		if [[ -e "$work/data/perf/qa-checks.jsonl" || -e "$work/home/.perf/grain-qa-checks.jsonl" ]]; then
			echo "FAIL: perf/qa-checks.jsonl was created while QA is off!" >&2
			exit 1
		fi
		echo "  perf JSONL:      absent (verified)"

		local api_qa
		api_qa=$(api GET /api/settings | jq -c '{qaEnabled, qaFlowId, qaRolloutPct, qaShadowOnControl, qaEpoch, qaRegimeId}')
		echo "  /api/settings:   $api_qa"
	fi
)

if [[ -x "$BASELINE_BIN" ]]; then
	run_five_dispatches "baseline-24d0eba1" "$BASELINE_BIN"
else
	echo "NOTE: $BASELINE_BIN not present; skipping baseline comparison"
fi
run_five_dispatches "head" "$HEAD_BIN"
