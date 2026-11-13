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

# Item 2 (§2): CLI round-trip for `grain qa status|enable|disable`, flag
# preservation, epoch monotonicity & range refusals, audit log + state-repo
# settings history, stopping assignment on disable, and restart persistence.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

work="/tmp/walk-qa-item2-${STORE:-sqlite}"
trap 'stop_env "$work"' EXIT

init_env "$work" "$GRAIN"
start_ghsim "$work"
start_daemon "$GRAIN" "$work"
sleep 1.2

qa_cli() {
	"$GRAIN" -server "$DAEMON" -token "$CODE" "$@"
}

assert_eq() {
	local label=$1 got=$2 want=$3
	if [[ "$got" != "$want" ]]; then
		echo "FAIL [$label]: got '$got', want '$want'" >&2
		exit 1
	fi
}

assert_fails() {
	local label=$1 want_substr=$2
	shift 2
	local out rc=0
	out=$("$GRAIN" -server "$DAEMON" -token "$CODE" "$@" 2>&1) || rc=$?
	if [[ "$rc" -eq 0 ]]; then
		echo "FAIL [$label]: expected non-zero exit, succeeded with: $out" >&2
		exit 1
	fi
	if [[ "$out" != *"$want_substr"* ]]; then
		echo "FAIL [$label]: error output '$out' did not contain '$want_substr'" >&2
		exit 1
	fi
	echo "  refused ($label): rc=$rc msg=$(echo "$out" | tr '\n' ' ')"
}

echo "=== [Item 2 / store=${STORE:-sqlite}] ==="

# 1. Fresh store status (human + JSON + bare `grain qa`)
status_txt=$(qa_cli qa status)
echo "$status_txt"
status_json=$(qa_cli --json qa)
assert_eq "fresh.enabled" "$(echo "$status_json" | jq -r '.enabled')" "false"
assert_eq "fresh.flowId" "$(echo "$status_json" | jq -r '.flowId')" "golden-v1-minimal"
assert_eq "fresh.rolloutPct" "$(echo "$status_json" | jq -r '.rolloutPct')" "0"
assert_eq "fresh.shadowOnControl" "$(echo "$status_json" | jq -r '.shadowOnControl')" "true"
assert_eq "fresh.epoch" "$(echo "$status_json" | jq -r '.epoch')" "1"
assert_eq "fresh.regimeId" "$(echo "$status_json" | jq -r '.regimeId')" "1"

audit_before=$(sql_scalar "SELECT COUNT(*) FROM config_audit WHERE surface = 'settings';")
git_commits_before=$(git -C "$work/data/state-repo" rev-list --count HEAD -- tables/grain_config.json)

# 2. enable -flow golden-v1-minimal -rollout 30 -> status
qa_cli qa enable -flow golden-v1-minimal -rollout 30 >/dev/null
s1=$(qa_cli --json qa status)
assert_eq "step2.enabled" "$(echo "$s1" | jq -r '.enabled')" "true"
assert_eq "step2.flowId" "$(echo "$s1" | jq -r '.flowId')" "golden-v1-minimal"
assert_eq "step2.rolloutPct" "$(echo "$s1" | jq -r '.rolloutPct')" "30"
assert_eq "step2.shadowOnControl" "$(echo "$s1" | jq -r '.shadowOnControl')" "true"
assert_eq "step2.epoch" "$(echo "$s1" | jq -r '.epoch')" "1"
assert_eq "step2.regimeId" "$(echo "$s1" | jq -r '.regimeId')" "1"
echo "  step2 enable 30%: $(echo "$s1" | jq -c .)"

# 3. enable -rollout 50 keeps flow and epoch, bumps regime -> 2
qa_cli qa enable -rollout 50 >/dev/null
s2=$(qa_cli --json qa status)
assert_eq "step3.enabled" "$(echo "$s2" | jq -r '.enabled')" "true"
assert_eq "step3.flowId" "$(echo "$s2" | jq -r '.flowId')" "golden-v1-minimal"
assert_eq "step3.rolloutPct" "$(echo "$s2" | jq -r '.rolloutPct')" "50"
assert_eq "step3.epoch" "$(echo "$s2" | jq -r '.epoch')" "1"
assert_eq "step3.regimeId" "$(echo "$s2" | jq -r '.regimeId')" "2"
echo "  step3 enable 50%: $(echo "$s2" | jq -c .)"

# 4. Non-default flow + shadow=false + epoch bump to 3, then partial update preserves all
qa_cli qa enable -flow repo-custom -shadow-control=false -epoch 3 >/dev/null
qa_cli qa enable -rollout 65 >/dev/null
s3=$(qa_cli --json qa status)
assert_eq "step4.flowId" "$(echo "$s3" | jq -r '.flowId')" "repo-custom"
assert_eq "step4.shadowOnControl" "$(echo "$s3" | jq -r '.shadowOnControl')" "false"
assert_eq "step4.rolloutPct" "$(echo "$s3" | jq -r '.rolloutPct')" "65"
assert_eq "step4.epoch" "$(echo "$s3" | jq -r '.epoch')" "3"
assert_eq "step4.regimeId" "$(echo "$s3" | jq -r '.regimeId')" "4"
echo "  step4 preserve custom flags: $(echo "$s3" | jq -c .)"

# 5. Refusals: -epoch 0, epoch downgrade (<3), -rollout 101, -rollout -1, bad flow
audit_pre_refusal=$(sql_scalar "SELECT COUNT(*) FROM config_audit WHERE surface = 'settings';")
assert_fails "epoch-zero" "qaEpoch must be at least 1" qa enable -epoch 0
assert_fails "epoch-negative" "qaEpoch must be at least 1" qa enable -epoch -2
assert_fails "epoch-downgrade" "epoch cannot decrease from 3 to 2" qa enable -epoch 2
assert_fails "rollout-101" "rolloutPct must be between 0 and 100" qa enable -rollout 101
assert_fails "rollout-neg" "rolloutPct must be between 0 and 100" qa enable -rollout -1
assert_fails "unknown-flow" "flowId must be one of" qa enable -flow nonexistent-flow
audit_post_refusal=$(sql_scalar "SELECT COUNT(*) FROM config_audit WHERE surface = 'settings';")
assert_eq "refusals-no-audit" "$audit_post_refusal" "$audit_pre_refusal"

# 6. Dispatch one task while QA is enabled -> assignment row + perf JSONL line written
resp_on=$(api POST /api/tasks '{"title":"Enabled QA task","body":"Check arm assignment when enabled","repo":"acme/widgets"}')
tid_on=$(echo "$resp_on" | jq -r '.id')
api POST "/api/tasks/${tid_on}/approve" '{}' >/dev/null
wait_task_closed "$tid_on"
assign_on=$(sql_scalar "SELECT COUNT(*) FROM task_qa_assignment;")
assert_eq "enabled.assignment_rows" "$assign_on" "1"
perf_lines_on=$(wc -l <"$work/data/perf/qa-checks.jsonl")
assert_eq "enabled.perf_lines" "$perf_lines_on" "1"
echo "  dispatched while enabled: assignment_rows=$assign_on perf_lines=$perf_lines_on"

# 7. disable keeps settings, bumps regime -> 5, and stops assignment on subsequent tasks
qa_cli qa disable >/dev/null
s_dis=$(qa_cli --json qa status)
assert_eq "disable.enabled" "$(echo "$s_dis" | jq -r '.enabled')" "false"
assert_eq "disable.flowId" "$(echo "$s_dis" | jq -r '.flowId')" "repo-custom"
assert_eq "disable.rolloutPct" "$(echo "$s_dis" | jq -r '.rolloutPct')" "65"
assert_eq "disable.shadowOnControl" "$(echo "$s_dis" | jq -r '.shadowOnControl')" "false"
assert_eq "disable.epoch" "$(echo "$s_dis" | jq -r '.epoch')" "3"
assert_eq "disable.regimeId" "$(echo "$s_dis" | jq -r '.regimeId')" "5"
echo "  step7 after disable: $(echo "$s_dis" | jq -c .)"

resp_off=$(api POST /api/tasks '{"title":"Disabled QA task","body":"Must not assign when disabled","repo":"acme/widgets"}')
tid_off=$(echo "$resp_off" | jq -r '.id')
api POST "/api/tasks/${tid_off}/approve" '{}' >/dev/null
wait_task_closed "$tid_off"
assign_after_dis=$(sql_scalar "SELECT COUNT(*) FROM task_qa_assignment;")
perf_lines_after_dis=$(wc -l <"$work/data/perf/qa-checks.jsonl")
assert_eq "disabled.assignment_stopped" "$assign_after_dis" "1"
assert_eq "disabled.perf_stopped" "$perf_lines_after_dis" "1"
echo "  dispatched after disable: assignment_rows=$assign_after_dis perf_lines=$perf_lines_after_dis (unchanged)"

# 8. Audit log & state-repo settings history
# 5 accepted QA writes occurred (step2, step3, step4a, step4b, step7).
audit_after=$(sql_scalar "SELECT COUNT(*) FROM config_audit WHERE surface = 'settings';")
audit_delta=$((audit_after - audit_before))
assert_eq "audit.delta" "$audit_delta" "5"
api_audit_len=$(api GET "/api/deployment/config-audit?limit=10" | jq '[.[] | select(.surface == "settings")] | length')
echo "  config_audit delta: $audit_delta (API /api/deployment/config-audit settings rows=$api_audit_len)"

# Ensure state repo exported and committed each change
git_commits_after=$(git -C "$work/data/state-repo" rev-list --count HEAD -- tables/grain_config.json)
git_delta=$((git_commits_after - git_commits_before))
echo "  state-repo grain_config.json commits delta: $git_delta"
if [[ "$git_delta" -lt 5 ]]; then
	echo "FAIL: expected at least 5 state-repo commits on tables/grain_config.json, got $git_delta" >&2
	exit 1
fi
exported_qa=$(jq -r '.[0].qa' "$work/data/state-repo/tables/grain_config.json")
echo "  exported grain_config.qa: $exported_qa"

# 9. Restart persistence
start_daemon "$GRAIN" "$work"
s_restart=$(qa_cli --json qa status)
assert_eq "restart.json" "$(echo "$s_restart" | jq -c .)" "$(echo "$s_dis" | jq -c .)"
echo "  survived daemon restart: $(echo "$s_restart" | jq -c .)"
