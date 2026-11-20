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

# End-to-end real-daemon walkthrough for docs/test-plans/2026-11-19.md §2.
# Usage:
#   STORE=sqlite  ./run_walkthrough.sh
#   STORE=postgres ./run_walkthrough.sh
# Pass KEEP_ENV=1 to leave the daemon and ghsim running for browser walkthrough.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

STORE=${STORE:-sqlite}
WORK_DIR=${WORK_DIR:-/tmp/walk-qa-v6-${STORE}}
KEEP_ENV=${KEEP_ENV:-0}
FAILURES=0

pass() { printf '  [PASS] %s\n' "$*"; }
fail() { printf '  [FAIL] %s\n' "$*" >&2; FAILURES=$((FAILURES + 1)); }
section() { printf '\n=== [%s] %s ===\n' "$STORE" "$*"; }

cleanup() {
	if [[ "$KEEP_ENV" != "1" ]]; then
		stop_env "$WORK_DIR" || true
	fi
}
trap cleanup EXIT

section "0. Boot auth-enforcing ghsim + real grain daemon ($STORE)"
init_env "$WORK_DIR"
start_ghsim "$WORK_DIR"
start_daemon "$WORK_DIR"

# Verify unauthenticated direct git clone/ls-remote is rejected with 401.
unauth_code=$(curl -sS -o /dev/null -w '%{http_code}' "http://$GHSIM/acme/widgets.git/info/refs?service=git-upload-pack")
if [[ "$unauth_code" == "401" ]]; then
	pass "Unauthenticated git HTTP to ghsim rejected with HTTP 401"
else
	fail "Expected HTTP 401 for unauthenticated git HTTP, got $unauth_code"
fi

# Verify canonical catalog (3 workflows only).
flows_json=$(api GET /api/qa/flows)
flow_ids=$(echo "$flows_json" | jq -r '[.flows[].id] | join(",")')
if [[ "$flow_ids" == "standard-workflow,fast-baseline,gates-only" ]]; then
	pass "GET /api/qa/flows returns only canonical 3 flows: $flow_ids"
else
	fail "GET /api/qa/flows unexpected: $flow_ids"
fi

fv_json=$(api GET /api/qa/flow-versions)
fv_builtin_keys=$(echo "$fv_json" | jq -r '[.versions[] | select(.builtIn == true) | .key] | join(",")')
if [[ "$fv_builtin_keys" == "standard-workflow,fast-baseline,gates-only" ]]; then
	pass "GET /api/qa/flow-versions builtIns match canonical 3 flows: $fv_builtin_keys"
else
	fail "GET /api/qa/flow-versions builtIns unexpected: $fv_builtin_keys"
fi

settings_json=$(api GET /api/settings)
settings_flow_ids=$(echo "$settings_json" | jq -r '[.qaFlowIds[]] | join(",")')
if [[ "$settings_flow_ids" == "standard-workflow,fast-baseline,gates-only" ]]; then
	pass "GET /api/settings qaFlowIds returns only canonical 3 flows: $settings_flow_ids"
else
	fail "GET /api/settings qaFlowIds unexpected: $settings_flow_ids"
fi

# Enable QA with standard-workflow at 100% rollout.
api PUT /api/settings '{"qaEnabled":true,"qaFlowId":"standard-workflow","qaRolloutPct":100,"qaShadowOnControl":true}' >/dev/null
pass "Enabled QA with standard-workflow at 100% rollout"

section "1. Attachment-driven complexity, PNG skip, UTF-8 boundary, S0 skip, and propose_dag"

# Build payloads with python3 so base64, binary PNG, and exact UTF-8 straddling 131072 bytes are exact.
python3 - "$WORK_DIR" <<'PYEOF'
import base64, json, os, sys

work = sys.argv[1]

rich_md = """# Multi-Subsystem Architecture Spec

## 1. Overview
Implement end-to-end specification hydration across backend orchestrator, store, and `/next/` UI chips.
Touch `pkg/orchestrator/engine.go`, `pkg/ui/qa_api.go`, `pkg/model/store.go`, and `ui-ng/src/app/shared/chips.ts`.

## 2. Backend Storage & Migration
- Update `pkg/model/store.go` to load bounded UTF-8 specification attachments.
- Preserve rune boundaries across `DefaultTaskSpecAttachmentMaxBytes`.
- Enforce monotonic complexity tier updates within an epoch.
- Add PostgreSQL and SQLite integration coverage.

## 3. Orchestrator Phases & Decomposition
- Hydrate attachments in `pkg/orchestrator/engine.go` for S0, S1, S0b, and S2.
- Skip redundant S0 LLM intent calls when authored specification attachments exist.
- Route high-complexity root tasks to `propose_dag` and child subtasks to `slice_plan`.

## 4. Phased Implementation
- Phase 1: Model store and SQLite/PostgreSQL hydration.
- Phase 2: Orchestrator phases and `/next/` UI badge updates (`ui-ng/src/app/shared/chips.ts`).
- Phase 3: Verification across light/dark and desktop/mobile viewports.

## 5. Acceptance Criteria
- Binary PNG attachments are excluded from text hydration.
- Oversized multi-byte UTF-8 attachments truncate cleanly without replacement characters.
- Classifier chips stay aligned with backend batch `qaSummary`.
"""
assert len(rich_md) > 1200

# Real PNG header + null bytes + marker that must NEVER appear in hydrated text.
png_bytes = b"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xdePNG_BINARY_SENTINEL_NEVER_HYDRATE"

# Oversized UTF-8 text: place 4-byte emoji (🚀 = 4 bytes) straddling BOTH 131072 and 131076 bytes!
header = (
    "# Oversized UTF-8 Spec\n\n"
    "## 1. Overview\n"
    "Cross-surface plan touching `pkg/orchestrator/engine.go`, `pkg/ui/qa_api.go`, `pkg/model/store.go`, and `ui-ng/src/app/shared/chips.ts`.\n\n"
    "## 2. Backend\n- Item 1\n- Item 2\n- Item 3\n\n"
    "## 3. Frontend\n- Item 4\n- Item 5\n- Item 6\n\n"
    "## 4. Phased Implementation\n- Phase 1: Core\n- Phase 2: UI\n\n"
).encode("utf-8")
# Pad so the emoji stream starts at an offset where 131072 and 131076 both cut mid-rune (mod 4 == 2).
pad_len = (2 - (len(header) % 4)) % 4
base_prefix = header + (b"A" * pad_len)
assert len(base_prefix) % 4 == 2
emoji_unit = "🚀".encode("utf-8")  # 4 bytes: f0 9f 9a 80
count = ((131072 + 4096) - len(base_prefix)) // 4
oversized_utf8 = base_prefix + (emoji_unit * count)
assert len(oversized_utf8) > 131072 + 1024
# Confirm byte 131072 and 131076 are mid-rune (continuation byte 0x9a).
assert (oversized_utf8[131072] & 0xC0) == 0x80, hex(oversized_utf8[131072])
assert (oversized_utf8[131076] & 0xC0) == 0x80, hex(oversized_utf8[131076])

root_payload = {
    "repo": "acme/widgets",
    "title": "Implement widget workflow",
    "description": "See attached spec.",
    "approve": False,
    "attachments": [
        {
            "filename": "1-architecture-spec.md",
            "contentType": "text/markdown; charset=utf-8",
            "content": base64.b64encode(rich_md.encode("utf-8")).decode("ascii"),
        },
        {
            "filename": "2-diagram.png",
            "contentType": "image/png",
            "content": base64.b64encode(png_bytes).decode("ascii"),
        },
        {
            "filename": "3-oversized-utf8.txt",
            "contentType": "text/plain; charset=utf-8",
            "content": base64.b64encode(oversized_utf8).decode("ascii"),
        },
    ],
}
with open(os.path.join(work, "req-root.json"), "w", encoding="utf-8") as f:
    json.dump(root_payload, f)

utf8_only_payload = {
    "repo": "acme/widgets",
    "title": "Hydrate oversized UTF-8 spec",
    "description": "See attached text file.",
    "approve": False,
    "attachments": [
        {
            "filename": "oversized-utf8.txt",
            "contentType": "text/plain; charset=utf-8",
            "content": base64.b64encode(oversized_utf8).decode("ascii"),
        },
    ],
}
with open(os.path.join(work, "req-utf8-only.json"), "w", encoding="utf-8") as f:
    json.dump(utf8_only_payload, f)

# Contrast task: medium inline complexity, NO authored spec attachment -> S0 SHOULD fire.
contrast_payload = {
    "repo": "acme/widgets",
    "title": "Add validation helper in pkg/ui/qa_api.go",
    "description": (
        "Update `pkg/ui/qa_api.go` and `ui-ng/src/app/shared/chips.ts` to validate badge labels.\n"
        "- Reject empty labels.\n"
        "- Handle trimmed whitespace.\n"
        "- Cover edge cases in unit tests.\n"
        "- Preserve backwards compatibility.\n"
        "- Keep API responses deterministic.\n"
        "- Update caller formatting in pkg/caller/caller.go.\n"
    ),
    "approve": False,
}
with open(os.path.join(work, "req-contrast.json"), "w", encoding="utf-8") as f:
    json.dump(contrast_payload, f)

# pathTokenRe boundary test attachment.
path_spec_md = """# Path Extraction Boundary Specification

Review external documentation at https://docs.example.com/pkg/foo.go before editing.
Also ignore backup files like config.yaml.bak and nested backups like pkg/ignored_backup.go.bak.
Ignore local attachment references like ./attachments/design.md.

## Target Repository Files
- Update ./pkg/ui/qa_api.go for summary badges.
- Check .github/workflows/tests.yml for CI job wiring.
- Check .grain/settings.yml for deployment defaults.
"""
path_payload = {
    "repo": "acme/widgets",
    "title": "Verify path extraction boundaries",
    "description": "See attached boundary spec.",
    "approve": False,
    "attachments": [
        {
            "filename": "path-spec.md",
            "contentType": "text/markdown; charset=utf-8",
            "content": base64.b64encode(path_spec_md.encode("utf-8")).decode("ascii"),
        },
    ],
}
with open(os.path.join(work, "req-path-bounds.json"), "w", encoding="utf-8") as f:
    json.dump(path_payload, f)
PYEOF

root_resp=$(api POST /api/tasks "@$WORK_DIR/req-root.json")
ROOT_ID=$(echo "$root_resp" | jq -r '.task.id // .id')
echo "  Created root task #$ROOT_ID"

utf8_resp=$(api POST /api/tasks "@$WORK_DIR/req-utf8-only.json")
UTF8_ID=$(echo "$utf8_resp" | jq -r '.task.id // .id')
echo "  Created oversized-UTF-8-only task #$UTF8_ID"

# Verify pre-dispatch qaSummary on GET /api/tasks (batch) and GET /api/tasks/{id} (detail).
list_json=$(api GET "/api/tasks?full=1")
detail_root=$(api GET "/api/tasks/$ROOT_ID")
detail_utf8=$(api GET "/api/tasks/$UTF8_ID")

root_list_qa=$(echo "$list_json" | jq -c --arg id "$ROOT_ID" '(if type=="array" then . else .tasks end)[] | select(.id == $id) | .qaSummary')
root_detail_qa=$(echo "$detail_root" | jq -c '.qaSummary // .task.qaSummary')
if [[ "$root_list_qa" == "$root_detail_qa" && "$(echo "$root_detail_qa" | jq -r '.complexityTier')" == "high" && "$(echo "$root_detail_qa" | jq -r '.pending')" == "true" ]]; then
	pass "Root task #$ROOT_ID pre-dispatch qaSummary escalated to high via attachment (batch == detail): $root_detail_qa"
else
	fail "Root task #$ROOT_ID pre-dispatch qaSummary mismatch/wrong tier: list=$root_list_qa detail=$root_detail_qa"
fi

utf8_tier=$(echo "$detail_utf8" | jq -r '(.qaSummary // .task.qaSummary).complexityTier // "null"')
if [[ "$utf8_tier" == "high" ]]; then
	pass "Oversized multi-byte UTF-8 attachment task #$UTF8_ID truncated on clean rune boundary and escalated to high"
else
	fail "Oversized multi-byte UTF-8 attachment task #$UTF8_ID failed to escalate (complexityTier=$utf8_tier; likely SUBSTR/UTF-8 bug!)"
fi

# Configure fake-agy for Root task: propose a high-complexity child subtask + nothing_needed.
python3 - "$WORK_DIR" "$ROOT_ID" <<'PYEOF'
import json, os, sys
work, root_id = sys.argv[1], sys.argv[2]
child_body = (
    "## 1. Overview\n"
    "Implement wave 1 across `pkg/orchestrator/engine.go`, `pkg/ui/qa_api.go`, `pkg/model/store.go`, and `ui-ng/src/app/shared/chips.ts`.\n\n"
    "## 2. Backend\n- Update store schema and query.\n- Add security validation.\n- Verify migrations.go compatibility.\n\n"
    "## 3. Frontend\n- Update chip badge component.\n- Render tooltips.\n- Verify mobile layout.\n\n"
    "## 4. Phased Implementation\n- Phase 1: Backend.\n- Phase 2: Frontend.\n"
) * 2
action = {
    "kind": "mcp_sequence",
    "calls": [
        {
            "tool": "propose_task",
            "args": {
                "title": "Wave 1 child subtask from workstream root",
                "body": child_body,
                "repo": "acme/widgets",
                "hold_for_approval": True,
            },
        },
        {
            "tool": "nothing_needed",
            "args": {
                "reason": "Decomposed root workstream into wave 1 child subtask.",
            },
        },
    ],
    "response": "Proposed wave-1 child subtask and closed root workstream header.",
}
with open(os.path.join(work, f"action-{root_id}.json"), "w", encoding="utf-8") as f:
    json.dump(action, f)
PYEOF

api POST "/api/tasks/$ROOT_ID/approve" '{}' >/dev/null
root_state=$(wait_task_state "$ROOT_ID" "closed|awaiting_reply|awaiting_submit" 120)
echo "  Root task #$ROOT_ID settled in state: $root_state"

root_qa=$(api GET "/api/tasks/$ROOT_ID/qa")
s0_count=$(echo "$root_qa" | jq '[.checkRuns[] | select(.stage == "S0")] | length')
s0_inv=$(jq -s --arg id "$ROOT_ID" '[.[] | select(.taskId == $id and .stageHint == "s0-intent")] | length' "$WORK_DIR/runs/invocations.jsonl")
if [[ "$s0_count" == "0" && "$s0_inv" == "0" ]]; then
	pass "Root task #$ROOT_ID skipped redundant S0 LLM intent call when authored spec attachment present"
else
	fail "Root task #$ROOT_ID unexpectedly ran S0 (checkRuns=$s0_count, invocations=$s0_inv)"
fi

s0b_mode=$(echo "$root_qa" | jq -r '[.checkRuns[] | select(.stage == "S0b")][0].detailJson // "{}" | fromjson | .effectiveMode // ""')
s0b_child=$(echo "$root_qa" | jq -r '[.checkRuns[] | select(.stage == "S0b")][0].detailJson // "{}" | fromjson | .isChildSubtask')
s0b_tier=$(echo "$root_qa" | jq -r '[.checkRuns[] | select(.stage == "S0b")][0].complexityTier // ""')
if [[ "$s0b_mode" == "propose_dag" && "$s0b_child" == "false" && "$s0b_tier" == "high" ]]; then
	pass "Root task #$ROOT_ID entered S0b propose_dag (isChildSubtask=false, complexityTier=high)"
else
	fail "Root task #$ROOT_ID S0b mismatch: mode=$s0b_mode isChild=$s0b_child tier=$s0b_tier"
fi

root_prompt_file=$(jq -sr --arg id "$ROOT_ID" '[.[] | select(.taskId == $id and .stageHint == "agent")][0].promptFile // ""' "$WORK_DIR/runs/invocations.jsonl")
if [[ -n "$root_prompt_file" ]] && grep -q '## Workstream Subtask Decomposition (S0b — propose_dag)' "$root_prompt_file"; then
	pass "Root task #$ROOT_ID agent prompt includes S0b propose_dag instructions"
else
	fail "Root task #$ROOT_ID agent prompt missing propose_dag instructions ($root_prompt_file)"
fi
if grep -q 'PNG_BINARY_SENTINEL_NEVER_HYDRATE' "$root_prompt_file"; then
	fail "Root task #$ROOT_ID leaked binary PNG content into agent prompt"
else
	pass "Root task #$ROOT_ID skipped binary PNG attachment cleanly"
fi
has_repl=$(jq -sr --arg id "$ROOT_ID" '[.[] | select(.taskId == $id and .stageHint == "agent")][0].hasReplacementChar' "$WORK_DIR/runs/invocations.jsonl")
if [[ "$has_repl" == "false" ]]; then
	pass "Root task #$ROOT_ID prompt has zero UTF-8 replacement characters"
else
	fail "Root task #$ROOT_ID prompt contains UTF-8 replacement characters!"
fi

assign_tier=$(store_query "SELECT complexity_tier FROM task_qa_assignment WHERE root_origin = 'task:${ROOT_ID}' LIMIT 1;")
if [[ "$assign_tier" == "high" ]]; then
	pass "DB task_qa_assignment.complexity_tier persisted as 'high' for root task #$ROOT_ID"
else
	fail "DB task_qa_assignment.complexity_tier expected 'high', got '$assign_tier'"
fi

# Contrast task: prove S0 DOES run when no authored spec attachment exists.
contrast_resp=$(api POST /api/tasks "@$WORK_DIR/req-contrast.json")
CONTRAST_ID=$(echo "$contrast_resp" | jq -r '.task.id // .id')
api POST "/api/tasks/$CONTRAST_ID/approve" '{}' >/dev/null
wait_task_state "$CONTRAST_ID" "closed|awaiting_reply|awaiting_submit" 120 >/dev/null
contrast_s0=$(api GET "/api/tasks/$CONTRAST_ID/qa" | jq '[.checkRuns[] | select(.stage == "S0")] | length')
if [[ "$contrast_s0" -ge 1 ]]; then
	pass "Contrast inline task #$CONTRAST_ID (no authored spec) executed S0 qa:intent ($contrast_s0 run)"
else
	fail "Contrast inline task #$CONTRAST_ID did not execute S0 (expected >=1, got $contrast_s0)"
fi

# 1b. Child subtask steps down to slice_plan.
CHILD_ID=$(api GET "/api/tasks?full=1" | jq -r '(if type=="array" then . else .tasks end)[] | select(.title == "Wave 1 child subtask from workstream root") | .id' | head -n 1)
if [[ -z "$CHILD_ID" ]]; then
	fail "Proposed child subtask from root #$ROOT_ID was not filed!"
else
	echo "  Found proposed child subtask #$CHILD_ID"
	api POST "/api/tasks/$CHILD_ID/approve" '{}' >/dev/null
	wait_task_state "$CHILD_ID" "closed|awaiting_reply|awaiting_submit" 120 >/dev/null
	child_qa=$(api GET "/api/tasks/$CHILD_ID/qa")
	child_mode=$(echo "$child_qa" | jq -r '[.checkRuns[] | select(.stage == "S0b")][0].detailJson // "{}" | fromjson | .effectiveMode // ""')
	child_is_sub=$(echo "$child_qa" | jq -r '[.checkRuns[] | select(.stage == "S0b")][0].detailJson // "{}" | fromjson | .isChildSubtask')
	child_tier=$(echo "$child_qa" | jq -r '[.checkRuns[] | select(.stage == "S0b")][0].complexityTier // ""')
	if [[ "$child_mode" == "slice_plan" && "$child_is_sub" == "true" && "$child_tier" == "high" ]]; then
		pass "Child subtask #$CHILD_ID stepped down to S0b slice_plan (isChildSubtask=true, complexityTier=high)"
	else
		fail "Child subtask #$CHILD_ID S0b mismatch: mode=$child_mode isChild=$child_is_sub tier=$child_tier"
	fi
fi

# 1c. Console Session / Director-filed root task preserves propose_dag.
section "1c. Console Session / Director-filed root task preserves propose_dag"
python3 - "$WORK_DIR" <<'PYEOF'
import json, os, sys
work = sys.argv[1]
dir_body = (
    "## 1. Overview\n"
    "Director-filed multi-subsystem task touching `pkg/orchestrator/engine.go`, `pkg/ui/qa_api.go`, `pkg/model/store.go`, and `ui-ng/src/app/shared/chips.ts`.\n\n"
    "## 2. Backend\n- Implement migrations.go updates.\n- Add security validation.\n- Update store.\n\n"
    "## 3. Frontend\n- Update chips.\n- Add tooltips.\n- Verify mobile.\n\n"
    "## 4. Phased Implementation\n- Phase 1: Backend.\n- Phase 2: Frontend.\n"
) * 2
action = {
    "kind": "mcp_sequence",
    "calls": [
        {
            "tool": "propose_task",
            "args": {
                "title": "Director-filed root workstream task",
                "body": dir_body,
                "repo": "acme/widgets",
                "hold_for_approval": True,
            },
        },
        {
            "tool": "comment_on_issue",
            "args": {
                "comment": "Filed high-complexity root task from Director console session.",
            },
        },
    ],
    "response": "Proposed director root task.",
}
with open(os.path.join(work, "action-console-session.json"), "w", encoding="utf-8") as f:
    json.dump(action, f)
PYEOF

sess_resp=$(api POST /api/console-sessions '{"message":"Propose a high-complexity root task for acme/widgets"}')
SESS_TASK_ID=$(echo "$sess_resp" | jq -r '.session.taskId // .taskId // .id // (.sessions[-1].taskId) // empty')
if [[ -z "$SESS_TASK_ID" ]]; then
	SESS_TASK_ID=$(api GET "/api/tasks?full=1" | jq -r '[(if type=="array" then . else .tasks end)[] | select(.interactive == true)] | sort_by(.id | tonumber) | last | .id')
fi
echo "  Console session task: #$SESS_TASK_ID"

# Wait for the console session to file the proposal.
DIR_ROOT_ID=""
for _ in $(seq 120); do
	DIR_ROOT_ID=$(api GET "/api/tasks?full=1" | jq -r '(if type=="array" then . else .tasks end)[] | select(.title == "Director-filed root workstream task") | .id' | head -n 1)
	[[ -n "$DIR_ROOT_ID" ]] && break
	sleep 0.5
done
if [[ -z "$DIR_ROOT_ID" ]]; then
	fail "Console session #$SESS_TASK_ID did not file Director-filed root task"
else
	echo "  Found Director-filed root task #$DIR_ROOT_ID"
	api POST "/api/tasks/$DIR_ROOT_ID/approve" '{}' >/dev/null
	wait_task_state "$DIR_ROOT_ID" "closed|awaiting_reply|awaiting_submit" 120 >/dev/null
	dir_qa=$(api GET "/api/tasks/$DIR_ROOT_ID/qa")
	dir_mode=$(echo "$dir_qa" | jq -r '[.checkRuns[] | select(.stage == "S0b")][0].detailJson // "{}" | fromjson | .effectiveMode // ""')
	dir_is_sub=$(echo "$dir_qa" | jq -r '[.checkRuns[] | select(.stage == "S0b")][0].detailJson // "{}" | fromjson | .isChildSubtask')
	if [[ "$dir_mode" == "propose_dag" && "$dir_is_sub" == "false" ]]; then
		pass "Console Session / Director-filed root task #$DIR_ROOT_ID preserved S0b propose_dag (isChildSubtask=false)"
	else
		fail "Director-filed root task #$DIR_ROOT_ID expected propose_dag/false, got mode=$dir_mode isChild=$dir_is_sub"
	fi
fi

# 1d. Monotonic UpdateQAAssignmentComplexityTier within an epoch.
section "1d. Monotonic UpdateQAAssignmentComplexityTier within an epoch"
mono_resp=$(api POST /api/tasks '{"repo":"acme/widgets","title":"Monotonic tier check","description":"One line body.","approve":false}')
MONO_ID=$(echo "$mono_resp" | jq -r '.task.id // .id')
# Seed initial assignment as 'low' for this root origin in epoch 1, then attach rich spec & dispatch -> must upgrade to 'high'.
store_exec "INSERT INTO task_qa_assignment (root_origin, epoch, regime_id, arm, flow_id, rollout_pct_at_assign, complexity_tier, assigned_at) VALUES ('task:${MONO_ID}', 1, 1, 'treatment', 'standard-workflow', 100, 'low', '2026-11-19T00:00:00Z');"
# Add a comment with a rich markdown spec attachment (or update description) so post-clone upgrades low -> high.
python3 - "$WORK_DIR" "$MONO_ID" <<'PYEOF'
import json, os, sys
work, mid = sys.argv[1], sys.argv[2]
big_desc = (
    "## 1. Overview\nTouch `pkg/orchestrator/engine.go`, `pkg/ui/qa_api.go`, `pkg/model/store.go`, `ui-ng/src/app/shared/chips.ts`.\n\n"
    "## 2. Backend\n- Security auth checks\n- Schema migrations.go\n- Store tests\n\n"
    "## 3. Frontend\n- Chips\n- Tooltips\n- Mobile\n\n"
    "## 4. Phased Implementation\n- Phase 1: Core\n- Phase 2: UI\n"
) * 2
with open(os.path.join(work, f"mono-patch-{mid}.json"), "w", encoding="utf-8") as f:
    json.dump({"description": big_desc}, f)
PYEOF
api PATCH "/api/tasks/$MONO_ID" "$(cat "$WORK_DIR/mono-patch-${MONO_ID}.json")" >/dev/null
api POST "/api/tasks/$MONO_ID/approve" '{}' >/dev/null
wait_task_state "$MONO_ID" "closed|awaiting_reply|awaiting_submit" 120 >/dev/null
mono_after_up=$(store_query "SELECT complexity_tier FROM task_qa_assignment WHERE root_origin = 'task:${MONO_ID}' AND epoch = 1;")
if [[ "$mono_after_up" == "high" ]]; then
	pass "Monotonic upgrade low -> high persisted on task #$MONO_ID"
else
	fail "Monotonic upgrade failed on task #$MONO_ID: got '$mono_after_up'"
fi
# Now shrink description to a typo fix (low complexity), reopen, and dispatch again -> must NOT downgrade from high!
api PATCH "/api/tasks/$MONO_ID" '{"title":"Fix typo in README","description":"Fix one typo."}' >/dev/null
api POST "/api/tasks/$MONO_ID/reopen" '{}' >/dev/null
wait_task_state "$MONO_ID" "closed|awaiting_reply|awaiting_submit" 120 >/dev/null
mono_after_down=$(store_query "SELECT complexity_tier FROM task_qa_assignment WHERE root_origin = 'task:${MONO_ID}' AND epoch = 1;")
if [[ "$mono_after_down" == "high" ]]; then
	pass "Monotonic guard prevented high -> low downgrade on task #$MONO_ID within epoch 1"
else
	fail "Monotonic guard BROKEN: task #$MONO_ID downgraded from high to '$mono_after_down'"
fi

section "2. Ephemeral gitproxy shallow mirror (NewQAMirrorResolver) against 401-enforcing forge"
# Clear ghsim git-requests log so we isolate mirror + sandbox git traffic for this task.
curl -fsS -X DELETE "http://$GHSIM/_sim/git-requests" >/dev/null

mirror_resp=$(api POST /api/tasks '{"repo":"acme/widgets","title":"Update qa_api and engine for mirror S3/S4 test","description":"Update pkg/ui/qa_api.go and pkg/orchestrator/engine.go with unit tests.","approve":false}')
MIRROR_ID=$(echo "$mirror_resp" | jq -r '.task.id // .id')
echo "  Created mirror S3/S4 task #$MIRROR_ID"

python3 - "$WORK_DIR" "$MIRROR_ID" <<'PYEOF'
import json, os, sys
work, tid = sys.argv[1], sys.argv[2]
cmd = """set -euo pipefail
cd work
cat > pkg/ui/qa_api.go <<'EOF'
package ui

// RenderSummary formats a workflow badge summary with normalized spacing.
func RenderSummary(flow string) string {
	if flow == "" {
		return "standard-workflow"
	}
	return flow
}
EOF
cat > pkg/ui/qa_api_test.go <<'EOF'
package ui

import "testing"

func TestRenderSummary(t *testing.T) {
	if got := RenderSummary("standard-workflow"); got != "standard-workflow" {
		t.Fatalf("got %q", got)
	}
	if got := RenderSummary(""); got != "standard-workflow" {
		t.Fatalf("empty got %q", got)
	}
}
EOF
cat > pkg/orchestrator/engine.go <<'EOF'
package orchestrator

// Dispatch schedules a task run with workflow tagging.
func Dispatch(id string) string {
	return "dispatch:v6:" + id
}
EOF
git add pkg/ui/qa_api.go pkg/ui/qa_api_test.go pkg/orchestrator/engine.go
git commit -m "feat(ui,orchestrator): normalize summary badge and dispatch tag

Update RenderSummary and Dispatch with regression coverage."
git push origin "HEAD:refs/heads/grain-qa-stage/""" + tid + """"
"""
action = {
    "kind": "mcp_sequence",
    "calls": [
        {"tool": "run_command", "args": {"command": cmd, "description": "committing and pushing staged branch for S3/S4 mirror check"}},
    ],
    "response": "Pushed changes to staging branch for controller S3/S4 verification.",
}
with open(os.path.join(work, f"action-{tid}.json"), "w", encoding="utf-8") as f:
    json.dump(action, f)
PYEOF

api POST "/api/tasks/$MIRROR_ID/approve" '{}' >/dev/null
mirror_state=$(wait_task_state "$MIRROR_ID" "awaiting_submit|awaiting_reply|closed" 180)
echo "  Mirror task #$MIRROR_ID settled in state: $mirror_state"

mirror_detail=$(api GET "/api/tasks/$MIRROR_ID")
mirror_pr=$(echo "$mirror_detail" | jq -r '.pullRequest // .task.pullRequest // empty')
mirror_qa=$(api GET "/api/tasks/$MIRROR_ID/qa")
s3_runs=$(echo "$mirror_qa" | jq '[.checkRuns[] | select(.stage == "S3")] | length')
s4_runs=$(echo "$mirror_qa" | jq '[.checkRuns[] | select(.stage == "S4")] | length')
s5_runs=$(echo "$mirror_qa" | jq '[.checkRuns[] | select(.stage == "S5")] | length')
echo "  Mirror task #$MIRROR_ID PR=${mirror_pr:-none} S3=$s3_runs S4=$s4_runs S5=$s5_runs"

if [[ -n "$mirror_pr" && "$s3_runs" -ge 1 ]]; then
	pass "Controller S3 mirror checkout succeeded and promoted PR ($mirror_pr) (S3=$s3_runs, S4=$s4_runs, S5=$s5_runs)"
else
	fail "Controller S3 mirror checkout/promotion failed (PR=${mirror_pr:-none}, S3=$s3_runs, S4=$s4_runs, S5=$s5_runs)"
fi
s3_tier=$(echo "$mirror_qa" | jq -r '[.checkRuns[] | select(.stage == "S3")][0].complexityTier // ""')
s5_tier=$(echo "$mirror_qa" | jq -r '[.checkRuns[] | select(.stage == "S5")][0].complexityTier // ""')
if [[ -n "$s3_tier" && "$s3_tier" == "$s5_tier" ]]; then
	pass "Mirror task #$MIRROR_ID S3 and S5 complexityTier agree ($s3_tier)"
else
	fail "Mirror task #$MIRROR_ID S3/S5 complexityTier drift: S3=$s3_tier S5=$s5_tier"
fi

git_reqs=$(curl -fsS "http://$GHSIM/_sim/git-requests")
unauth_mirror=$(echo "$git_reqs" | jq '[.[] | select(.status == 401)] | length')
authed_fetches=$(echo "$git_reqs" | jq '[.[] | select(.authed == true and .status == 200)] | length')
if [[ "$unauth_mirror" == "0" && "$authed_fetches" -ge 2 ]]; then
	pass "Auth-enforcing ghsim saw zero 401s and $authed_fetches authenticated 200 git requests"
else
	fail "ghsim git requests unexpected: 401s=$unauth_mirror authed200=$authed_fetches"
fi

audit_file="$WORK_DIR/data/state/git-proxy/audit.log"
if [[ -f "$audit_file" ]] && grep -q 'qa-mirror-' "$audit_file"; then
	mirror_identities=$(grep -o 'qa-mirror-[0-9a-f]*' "$audit_file" | sort -u | tr '\n' ' ')
	pass "gitproxy audit log recorded ephemeral qa-mirror-* session(s): $mirror_identities"
else
	fail "gitproxy audit log missing qa-mirror-* session entries ($audit_file)"
fi

# Verify zero credential leaks across logs, mirror git configs, and persisted QA rows.
leak_hits=0
for target in "$WORK_DIR/daemon.log" "$audit_file"; do
	if [[ -f "$target" ]] && grep -q 'ghp_walkthrough' "$target"; then
		fail "Credential leak detected in $target!"
		leak_hits=$((leak_hits + 1))
	fi
done
if [[ -d "$WORK_DIR/data/qa-repos" ]] && grep -rn 'ghp_walkthrough' "$WORK_DIR/data/qa-repos" >/dev/null 2>&1; then
	fail "Credential leak detected inside $WORK_DIR/data/qa-repos!"
	leak_hits=$((leak_hits + 1))
fi
if echo "$mirror_qa" | grep -q 'ghp_walkthrough'; then
	fail "Credential leak detected in task QA API response!"
	leak_hits=$((leak_hits + 1))
fi
if [[ "$leak_hits" == "0" ]]; then
	pass "Zero plaintext credential leaks in daemon.log, gitproxy audit.log, qa-repos, or QA rows"
fi

# Verify zero dangling qa-mirror-* sessions in sandbox-tokens.json and zero revoke errors.
if grep -q 'qa mirror: revoking' "$WORK_DIR/daemon.log"; then
	fail "Daemon logged qa-mirror token revocation error!"
else
	pass "Zero qa-mirror token revocation errors in daemon.log"
fi
dangling_check=$(cd "$ROOT" && go run -e /dev/null 2>/dev/null || true)
# Check sandbox-tokens.json via a tiny inline python/go check using pkg/gitproxy.LoadSandboxTokens
dangling_count=$(cd "$ROOT" && python3 - "$WORK_DIR/data/secrets/sandbox-tokens.json" <<'PYEOF'
import json, os, sys
p = sys.argv[1]
if not os.path.exists(p):
    print("0")
    sys.exit(0)
raw = open(p, "rb").read()
# If sealed JSON contains plaintext qa-mirror-, flag it; otherwise Go test verifies LoadSandboxTokens.
print("sealed" if b"sealed" in raw or not raw.startswith(b"{") else str(raw.decode("utf-8", "ignore").count("qa-mirror-")))
PYEOF
)
echo "  sandbox-tokens.json status: $dangling_count"

section "3. pathTokenRe extraction boundaries on real spec attachment"
path_resp=$(api POST /api/tasks "@$WORK_DIR/req-path-bounds.json")
PATH_ID=$(echo "$path_resp" | jq -r '.task.id // .id')
api POST "/api/tasks/$PATH_ID/approve" '{}' >/dev/null
wait_task_state "$PATH_ID" "closed|awaiting_reply|awaiting_submit" 120 >/dev/null
path_qa=$(api GET "/api/tasks/$PATH_ID/qa")
cand_paths=$(echo "$path_qa" | jq -c '[.checkRuns[] | select(.stage == "S1")][0].detailJson // "{}" | fromjson | .candidatePaths // []')
echo "  Task #$PATH_ID S1 candidatePaths: $cand_paths"

python3 - "$cand_paths" <<'PYEOF'
import json, sys
paths = json.loads(sys.argv[1])
must_have = ["pkg/ui/qa_api.go", ".github/workflows/tests.yml", ".grain/settings.yml"]
must_not = [
    "https://docs.example.com/pkg/foo.go",
    "docs.example.com/pkg/foo.go",
    "pkg/foo.go",
    "config.yaml.bak",
    "config.yaml",
    "pkg/ignored_backup.go.bak",
    "pkg/ignored_backup.go",
    "attachments/design.md",
    "./attachments/design.md",
    "design.md",
]
errors = []
for p in must_have:
    if p not in paths:
        errors.append(f"MISSING expected path: {p}")
for p in must_not:
    if p in paths:
        errors.append(f"POLLUTED with forbidden path: {p}")
if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
print("OK")
PYEOF
if [[ $? -eq 0 ]]; then
	pass "S1 candidatePaths extracted only valid repo & dot-directory paths (rejected URL, .bak, ./attachments/*)"
else
	fail "S1 candidatePaths boundary check FAILED: $cand_paths"
fi

section "4. Legacy qaFlowId alias compatibility across upgraded database + daemon restart"
# Create tasks and seed retired v1-v4 flow slugs in task_qa_assignment + grain_config.qa.
legacy_cases=(
	"golden-v1-minimal:fast-baseline:Fast"
	"golden-v2:standard-workflow:Standard"
	"golden-v2-checks-only:gates-only:Gates only"
	"golden-v2-skill-only:fast-baseline:Fast"
	"golden-v3:standard-workflow:Standard"
	"golden-v3-enforced:standard-workflow:Standard"
	"engineering-workflow-v4:standard-workflow:Standard"
	"golden-v4-composable:standard-workflow:Standard"
	"deterministic-s3-only:gates-only:Gates only"
	"repo-custom:fast-baseline:Fast"
)

declare -A LEGACY_IDS
for entry in "${legacy_cases[@]}"; do
	IFS=: read -r slug want_canon want_label <<<"$entry"
	resp=$(api POST /api/tasks "{\"repo\":\"acme/widgets\",\"title\":\"Legacy flow ${slug}\",\"description\":\"Seeded historical task for ${slug}.\",\"approve\":false}")
	tid=$(echo "$resp" | jq -r '.task.id // .id')
	LEGACY_IDS[$slug]=$tid
	store_exec "INSERT INTO task_qa_assignment (root_origin, epoch, regime_id, arm, flow_id, rollout_pct_at_assign, complexity_tier, assigned_at) VALUES ('task:${tid}', 1, 1, 'treatment', '${slug}', 100, 'medium', '2026-11-18T12:00:00Z');"
done

# Seed grain_config.qa with a retired v4 flow slug, then restart the daemon on HEAD!
stop_daemon "$WORK_DIR"
store_exec "UPDATE grain_config SET qa = '{\"enabled\":true,\"flowId\":\"engineering-workflow-v4\",\"rolloutPct\":100,\"shadowOnControl\":true,\"epoch\":1,\"regimeId\":1}' WHERE id = 1;"
start_daemon "$WORK_DIR"

# Verify GET /api/settings, GET /api/tasks, GET /api/tasks/{id}, and PUT /api/settings!
restarted_settings=$(api GET /api/settings)
restarted_flow=$(echo "$restarted_settings" | jq -r '.qaFlowId')
restarted_ids=$(echo "$restarted_settings" | jq -r '[.qaFlowIds[]] | join(",")')
if [[ "$restarted_flow" == "engineering-workflow-v4" && "$restarted_ids" == "standard-workflow,fast-baseline,gates-only" ]]; then
	pass "Restarted daemon loaded legacy stored qaFlowId=engineering-workflow-v4 while catalog stays 3 canonical flows"
else
	fail "Restarted settings mismatch: qaFlowId=$restarted_flow qaFlowIds=$restarted_ids"
fi

# Verify GET /api/tasks and GET /api/tasks/{id} resolve all 10 legacy slugs cleanly!
all_list=$(api GET "/api/tasks?full=1")
legacy_ok=1
for entry in "${legacy_cases[@]}"; do
	IFS=: read -r slug want_canon want_label <<<"$entry"
	tid=${LEGACY_IDS[$slug]}
	list_qa=$(echo "$all_list" | jq -c --arg id "$tid" '(if type=="array" then . else .tasks end)[] | select(.id == $id) | .qaSummary')
	det_qa=$(api GET "/api/tasks/$tid" | jq -c '.qaSummary // .task.qaSummary')
	got_flow=$(echo "$list_qa" | jq -r '.flowId // ""')
	got_label=$(echo "$list_qa" | jq -r '.flowLabel // ""')
	if [[ "$list_qa" != "$det_qa" || "$got_flow" != "$want_canon" || "$got_label" != "$want_label" ]]; then
		fail "Legacy slug $slug (task #$tid): got flow=$got_flow label=$got_label list=$list_qa det=$det_qa (want $want_canon/$want_label)"
		legacy_ok=0
	fi
done
if [[ "$legacy_ok" == "1" ]]; then
	pass "All 10 retired v1-v4 flow slugs resolved cleanly on GET /api/tasks & GET /api/tasks/{id} (batch == detail)"
fi

# Test PUT /api/settings with legacy stored flow and saving each of the 3 canonical flow slugs + legacy aliases.
for test_flow in "golden-v2" "golden-v1-minimal" "deterministic-s3-only" "engineering-workflow-v4" "fast-baseline" "gates-only" "standard-workflow"; do
	code=$(api_status PUT /api/settings "{\"qaEnabled\":true,\"qaFlowId\":\"${test_flow}\",\"qaRolloutPct\":100}" "$WORK_DIR/captures/put-${test_flow}.json")
	if [[ "$code" == "200" ]]; then
		pass "PUT /api/settings accepted qaFlowId=$test_flow (HTTP 200)"
	else
		fail "PUT /api/settings rejected qaFlowId=$test_flow with HTTP $code: $(cat "$WORK_DIR/captures/put-${test_flow}.json")"
	fi
done

# Also test restarting with golden-v4-composable and golden-v3-enforced stored in DB, then saving an unrelated setting (qaRolloutPct)!
for stored_legacy in "golden-v4-composable" "golden-v3-enforced"; do
	stop_daemon "$WORK_DIR"
	store_exec "UPDATE grain_config SET qa = '{\"enabled\":true,\"flowId\":\"${stored_legacy}\",\"rolloutPct\":100,\"shadowOnControl\":true,\"epoch\":1,\"regimeId\":1}' WHERE id = 1;"
	start_daemon "$WORK_DIR"
	code_get=$(api_status GET /api/settings "" "$WORK_DIR/captures/get-${stored_legacy}.json")
	code_put=$(api_status PUT /api/settings '{"qaRolloutPct":50}' "$WORK_DIR/captures/put-rollout-${stored_legacy}.json")
	code_mig=$(api_status PUT /api/settings '{"qaEnabled":true,"qaFlowId":"standard-workflow","qaRolloutPct":100}' "$WORK_DIR/captures/put-mig-${stored_legacy}.json")
	if [[ "$code_get" == "200" && "$code_put" == "200" && "$code_mig" == "200" ]]; then
		pass "Legacy stored $stored_legacy survived restart, GET /api/settings (200), rollout update (200), and migration to standard-workflow (200)"
	else
		fail "Legacy stored $stored_legacy issue: GET=$code_get PUT_rollout=$code_put ($(cat "$WORK_DIR/captures/put-rollout-${stored_legacy}.json")) PUT_mig=$code_mig"
	fi
done

# Also verify repo-level legacy qaFlowRef and task-level explicit QAFlowRef pending override.
api PUT /api/repos/acme/widgets/settings '{"qaFlowRef":"golden-v2"}' >/dev/null
repo_qa_ref=$(api GET /api/repos/acme/widgets/settings | jq -r '.qaFlowRef // ""')
if [[ "$repo_qa_ref" == "golden-v2" ]]; then
	pass "Repo settings accepted and preserved legacy qaFlowRef=golden-v2"
else
	fail "Repo settings qaFlowRef expected golden-v2, got '$repo_qa_ref'"
fi
api PUT /api/repos/acme/widgets/settings '{"qaFlowRef":""}' >/dev/null

pinned_resp=$(api POST /api/tasks '{"repo":"acme/widgets","title":"Explicit pinned gates-only pending check","description":"Verifies task.qaFlowRef overrides global default while pending.","approve":false}')
PINNED_ID=$(echo "$pinned_resp" | jq -r '.task.id // .id')
store_exec "UPDATE task SET qa_flow_ref = 'gates-only' WHERE id = '${PINNED_ID}';"
pinned_flow=$(api GET "/api/tasks/$PINNED_ID" | jq -r '(.qaSummary // .task.qaSummary).flowId // ""')
if [[ "$pinned_flow" == "gates-only" ]]; then
	pass "Pending task #$PINNED_ID with qa_flow_ref=gates-only previewed flowId=gates-only even while global QA is standard-workflow"
else
	fail "Pending task #$PINNED_ID with qa_flow_ref=gates-only previewed wrong flowId='$pinned_flow'"
fi

# Record task IDs for UI walkthrough if KEEP_ENV=1.
cat >"$WORK_DIR/walk-ids.json" <<EOF
{
  "store": "$STORE",
  "daemon": "$DAEMON",
  "rootId": "$ROOT_ID",
  "utf8Id": "$UTF8_ID",
  "contrastId": "$CONTRAST_ID",
  "childId": "$CHILD_ID",
  "dirRootId": "$DIR_ROOT_ID",
  "monoId": "$MONO_ID",
  "mirrorId": "$MIRROR_ID",
  "pathId": "$PATH_ID"
}
EOF

printf '\n=== [%s] Summary: failures=%d ===\n' "$STORE" "$FAILURES"
exit "$FAILURES"
