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

# Item 5 (§2): Upgrade a real schema-139 store (24d0eba1) through migrations
# 140..146 on SQLite and PostgreSQL with QA unset, verify `grain state check`,
# and verify a state-repo dump/import round-trip keeps `grain_config.qa` while
# leaving out the local-only `task_qa_*` tables.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

BASELINE_BIN=${BASELINE_BIN:-/home/debian/bin/grain-24d0eba1}
HEAD_BIN=${HEAD_BIN:-/home/debian/bin/grain}

if [[ ! -x "$BASELINE_BIN" ]]; then
	echo "ERROR: baseline schema-139 binary missing at $BASELINE_BIN" >&2
	exit 1
fi

work="/tmp/walk-qa-item5-${STORE:-sqlite}"
work_imp="/tmp/walk-qa-item5-imp-${STORE:-sqlite}"
cleanup() {
	stop_env "$work"
	stop_env "$work_imp"
}
trap cleanup EXIT

assert_eq() {
	local label=$1 got=$2 want=$3
	if [[ "$got" != "$want" ]]; then
		echo "FAIL [$label]: got '$got', want '$want'" >&2
		exit 1
	fi
}

echo "=== [Item 5 / store=${STORE:-sqlite}] ==="

# 1. Start schema-139 daemon (24d0eba1) and seed rows across 142/144/145 tables with QA unset
init_env "$work" "$BASELINE_BIN"
start_ghsim "$work"
start_daemon "$BASELINE_BIN" "$work"
sleep 1.2

ver_before=$(sql_scalar "SELECT MAX(version) FROM grain_schema;")
assert_eq "schema.before" "$ver_before" "139"
echo "  started baseline 24d0eba1 daemon at schema=$ver_before"

# Seed a template + template-backed schedule (migration 144) and release/qualification rows (142, 145)
tpl_resp=$(api POST /api/templates '{"name":"qa-walk-tpl","title":"Walk {{component}}","body":"Inspect {{component}}","repo":"acme/widgets"}')
tpl_id=$(echo "$tpl_resp" | jq -r '.id')
api POST /api/schedules "{\"name\":\"qa-walk-sched\",\"cron\":\"0 4 * * *\",\"templateId\":\"$tpl_id\",\"repo\":\"acme/widgets\",\"enabled\":false}" >/dev/null
sql_scalar "INSERT INTO qualification_config (owner, name, require_approval, auto_promote) VALUES ('acme', 'widgets', FALSE, TRUE);" >/dev/null
sql_scalar "INSERT INTO release (owner, repo, name, status, created_at) VALUES ('acme', 'widgets', 'v1.0.0', 'open', '2026-11-11T12:00:00Z');" >/dev/null

stop_env "$work"

# 2. Upgrade in-place 139 -> 146 by starting HEAD daemon + ghsim on the same store
start_ghsim "$work"
start_daemon "$HEAD_BIN" "$work"
sleep 1.2

ver_after=$(sql_scalar "SELECT MAX(version) FROM grain_schema;")
assert_eq "schema.after" "$ver_after" "146"
qa_col_unset=$(sql_scalar "SELECT qa FROM grain_config WHERE id = 1;")
assert_eq "qa.unset_after_upgrade" "$qa_col_unset" ""
echo "  upgraded in-place 139 -> $ver_after (grain_config.qa='$qa_col_unset')"

qa_status_unset=$("$HEAD_BIN" -server "$DAEMON" -token "$CODE" --json qa status)
assert_eq "qa.unset.enabled" "$(echo "$qa_status_unset" | jq -r '.enabled')" "false"
assert_eq "qa.unset.rolloutPct" "$(echo "$qa_status_unset" | jq -r '.rolloutPct')" "0"

"$HEAD_BIN" state -data-dir "$work/data" sync
check_unset_out=$("$HEAD_BIN" state check "$work/data/state-repo")
echo "  grain state check (QA unset): $(echo "$check_unset_out" | head -n 1)"

# 3. Configure QA and populate local-only task_qa_* tables on the upgraded store
"$HEAD_BIN" -server "$DAEMON" -token "$CODE" qa enable -flow deterministic-s3-only -rollout 35 -shadow-control=false -epoch 2 >/dev/null

t_resp=$(api POST /api/tasks '{"title":"Post-upgrade QA task","body":"Verify upgraded task_qa_* tables on 139->146 store","repo":"acme/widgets","origin":"acme/widgets#501"}')
tid=$(echo "$t_resp" | jq -r '.id')
api POST "/api/tasks/${tid}/approve" '{}' >/dev/null
wait_task_closed "$tid" 150

sql_scalar "INSERT INTO task_qa_check_run (task_id, run_id, commit_sha, stage, check_id, arm, shadow, verdict, duration_ms, alloc_bytes, detail_json, created_at) VALUES ('$tid', '${tid}-1', 'abcdef0123456789abcdef0123456789abcdef01', 'S3', 'hollow-test-ast', 'treatment', 0, 'fail', 42, 8192, '{\"files\":1}', '2026-11-13T12:00:00Z');" >/dev/null
cr_id=$(sql_scalar "SELECT MAX(id) FROM task_qa_check_run;")
sql_scalar "INSERT INTO task_qa_finding (check_run_id, task_id, stage, rule_id, severity, file_path, line, summary, evidence, human_vote, voted_by, created_at) VALUES ($cr_id, '$tid', 'S3', 'go-test-zero-assertions', 'blocker', 'pkg/foo/foo_test.go', 12, 'Test has zero assertions', 'func TestFoo(t *testing.T) {}', 1, 'alice@example.com', '2026-11-13T12:00:01Z');" >/dev/null

assert_eq "src.assign_rows" "$(sql_scalar "SELECT COUNT(*) FROM task_qa_assignment;")" "1"
assert_eq "src.check_run_rows" "$(sql_scalar "SELECT COUNT(*) FROM task_qa_check_run;")" "1"
assert_eq "src.finding_rows" "$(sql_scalar "SELECT COUNT(*) FROM task_qa_finding;")" "1"

# 4. Sync state-repo and verify `grain state check`, `grain_config.qa` kept, and `task_qa_*` NOT exported
"$HEAD_BIN" state -data-dir "$work/data" sync
check_set_out=$("$HEAD_BIN" state check "$work/data/state-repo")
echo "  grain state check (QA configured): $(echo "$check_set_out" | head -n 1)"

exported_qa=$(jq -r '.[0].qa' "$work/data/state-repo/tables/grain_config.json")
echo "  exported grain_config.qa: $exported_qa"
assert_eq "exported.qa.flowId" "$(echo "$exported_qa" | jq -r '.flowId')" "deterministic-s3-only"
assert_eq "exported.qa.rolloutPct" "$(echo "$exported_qa" | jq -r '.rolloutPct')" "35"
assert_eq "exported.qa.shadowOnControl" "$(echo "$exported_qa" | jq -r '.shadowOnControl')" "false"
assert_eq "exported.qa.epoch" "$(echo "$exported_qa" | jq -r '.epoch')" "2"

for tbl in task_qa_assignment task_qa_check_run task_qa_finding; do
	if [[ -e "$work/data/state-repo/tables/${tbl}.json" ]]; then
		echo "FAIL: local-only table ${tbl}.json was exported into state-repo!" >&2
		exit 1
	fi
done
echo "  verified: task_qa_assignment.json, task_qa_check_run.json, task_qa_finding.json absent from state-repo/tables/"

# Also verify `grain state check` refuses malformed/out-of-bounds grain_config.qa in a scratch copy
bad_dir=$(mktemp -d)
cp -a "$work/data/state-repo/." "$bad_dir/"
jq '.[0].qa = "{\"enabled\":true,\"rolloutPct\":999}"' "$bad_dir/tables/grain_config.json" >"$bad_dir/tables/grain_config.json.tmp"
mv "$bad_dir/tables/grain_config.json.tmp" "$bad_dir/tables/grain_config.json"
rc_bad=0
bad_out=$("$HEAD_BIN" state check "$bad_dir" 2>&1) || rc_bad=$?
rm -rf "$bad_dir"
if [[ "$rc_bad" -eq 0 || "$bad_out" != *"rolloutPct"* ]]; then
	echo "FAIL: expected grain state check to refuse rolloutPct=999 in grain_config.qa, got rc=$rc_bad out=$bad_out" >&2
	exit 1
fi
echo "  grain state check refused broken grain_config.qa (rc=$rc_bad)"

stop_env "$work"

# 5. Import round-trip into a brand-new store (SQLite or second Postgres DB) via restore-snapshot
ORIG_PG_DB="$PG_DB"
if [[ ${STORE:-sqlite} == postgres ]]; then
	PG_DB="${ORIG_PG_DB}_imp"
fi
DAEMON_PORT=4282 GHSIM_PORT=4286
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"

init_env "$work_imp" "$HEAD_BIN"
start_ghsim "$work_imp"
start_daemon "$HEAD_BIN" "$work_imp"
stop_env "$work_imp"

"$HEAD_BIN" state -data-dir "$work_imp/data" restore-snapshot "$work/data/state-repo" >/dev/null
start_ghsim "$work_imp"
start_daemon "$HEAD_BIN" "$work_imp"
sleep 1.0

imp_status=$("$HEAD_BIN" -server "$DAEMON" -token "$CODE" --json qa status)
echo "  imported store qa status: $(echo "$imp_status" | jq -c .)"
assert_eq "imp.enabled" "$(echo "$imp_status" | jq -r '.enabled')" "true"
assert_eq "imp.flowId" "$(echo "$imp_status" | jq -r '.flowId')" "deterministic-s3-only"
assert_eq "imp.rolloutPct" "$(echo "$imp_status" | jq -r '.rolloutPct')" "35"
assert_eq "imp.shadowOnControl" "$(echo "$imp_status" | jq -r '.shadowOnControl')" "false"
assert_eq "imp.epoch" "$(echo "$imp_status" | jq -r '.epoch')" "2"

assert_eq "imp.assign_rows" "$(sql_scalar "SELECT COUNT(*) FROM task_qa_assignment;")" "0"
assert_eq "imp.check_run_rows" "$(sql_scalar "SELECT COUNT(*) FROM task_qa_check_run;")" "0"
assert_eq "imp.finding_rows" "$(sql_scalar "SELECT COUNT(*) FROM task_qa_finding;")" "0"
echo "  verified imported store has grain_config.qa intact and 0 rows in task_qa_* tables"
