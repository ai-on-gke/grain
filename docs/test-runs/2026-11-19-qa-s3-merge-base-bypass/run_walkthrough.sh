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

# End-to-end real-daemon walkthrough for docs/test-plans/2026-11-19.md §3.
# Run with STORE=sqlite or STORE=postgres. Set KEEP_RUNNING=1 to keep the
# daemon and ghsim alive after completion (e.g. for Playwright UI checks).

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=./env.sh
source "$HERE/env.sh"

STORE=${STORE:-sqlite}
WORK_DIR=${WORK_DIR:-/tmp/walk-qa-s3-${STORE}}
KEEP_RUNNING=${KEEP_RUNNING:-0}

PASS_COUNT=0
FAIL_COUNT=0
FINDINGS=()

ok() {
	PASS_COUNT=$((PASS_COUNT + 1))
	printf "  [PASS] %s\n" "$1"
}

fail() {
	FAIL_COUNT=$((FAIL_COUNT + 1))
	FINDINGS+=("$1")
	printf "  [FAIL] %s\n" "$1" >&2
}

assert_eq() {
	local desc=$1 got=$2 want=$3
	if [[ "$got" == "$want" ]]; then
		ok "$desc ($got)"
	else
		fail "$desc: got=$got want=$want"
	fi
}

assert_contains() {
	local desc=$1 haystack=$2 needle=$3
	if [[ "$haystack" == *"$needle"* ]]; then
		ok "$desc"
	else
		fail "$desc: missing '$needle' in '$haystack'"
	fi
}

http_status_and_body() {
	local out_file=$1
	shift
	curl -sS -o "$out_file" -w "%{http_code}" "$@"
}

wait_task_state() {
	local tid=$1 want_re=$2 max_tries=${3:-90}
	local st=""
	for _ in $(seq 1 "$max_tries"); do
		st=$(api GET "/api/tasks/$tid" | jq -r '.state')
		if [[ "$st" =~ ^($want_re)$ ]]; then
			echo "$st"
			return 0
		fi
		sleep 0.3
	done
	echo "$st"
	return 1
}

wait_task_attempt_done() {
	local tid=$1 want_attempt=${2:-1} max_tries=${3:-100}
	for _ in $(seq 1 "$max_tries"); do
		local fin
		fin=$(api GET "/api/tasks/$tid" | jq -r --argjson n "$want_attempt" '[.attempts[]? | select(.number == $n and (.finishedAt // "") != "")] | length')
		if [[ "$fin" -ge 1 ]]; then
			return 0
		fi
		sleep 0.3
	done
	return 1
}

wait_task_pr() {
	local tid=$1 max_tries=${2:-80}
	local pr=""
	for _ in $(seq 1 "$max_tries"); do
		pr=$(api GET "/api/tasks/$tid" | jq -r '.pullRequest // ""')
		if [[ -n "$pr" ]]; then
			echo "$pr"
			return 0
		fi
		sleep 0.3
	done
	echo ""
	return 1
}

push_commit_to_bare() {
	local bare=$1 base_ref=$2 target_branch=$3 relpath=$4 content=$5 msg=$6
	local tmp sha
	tmp=$(mktemp -d)
	git -C "$tmp" init -q
	git -C "$tmp" remote add origin "$bare"
	git -C "$tmp" fetch -q origin "$base_ref"
	git -C "$tmp" checkout -q -B work FETCH_HEAD
	mkdir -p "$tmp/$(dirname "$relpath")"
	printf "%s" "$content" >"$tmp/$relpath"
	git -C "$tmp" add "$relpath"
	git -C "$tmp" -c user.name="Walk Agent" -c user.email="agent@example.com" commit -qm "$msg"
	sha=$(git -C "$tmp" rev-parse HEAD)
	git -C "$tmp" push -q -f origin "HEAD:refs/heads/$target_branch"
	rm -rf "$tmp"
	echo "$sha"
}

cleanup() {
	if [[ "$KEEP_RUNNING" != "1" ]]; then
		stop_env "$WORK_DIR"
	fi
}
trap cleanup EXIT

printf "=== Walkthrough §3 on STORE=%s (WORK_DIR=%s) ===\n" "$STORE" "$WORK_DIR"
init_env "$WORK_DIR"
start_ghsim "$WORK_DIR"
start_daemon "$WORK_DIR"

# shellcheck disable=SC1090
source "$WORK_DIR/shas.env"
printf "Seeded acme/widgets: ROOT=%s FORK=%s MAIN=%s\n" "${ROOT_SHA:0:12}" "${FORK_SHA:0:12}" "${MAIN_SHA:0:12}"

# Enable standard-workflow QA at 100% treatment rollout (stagingIsolation enabled).
api PUT /api/settings '{"qaEnabled":true,"qaFlowId":"standard-workflow","qaRolloutPct":100,"qaShadowOnControl":false}' >"$WORK_DIR/captures/settings-qa.json"
ok "Enabled QA standard-workflow at 100% rollout on $STORE"

# Verify QAEngineRev=7 flow catalog severities.
FLOWS_JSON=$(api GET /api/qa/flows)
echo "$FLOWS_JSON" >"$WORK_DIR/captures/qa-flows.json"
for flow_id in standard-workflow fast-baseline gates-only; do
	sev_dec=$(echo "$FLOWS_JSON" | jq -r --arg f "$flow_id" '.flows[] | select(.id==$f) | .rules[] | select(.id=="golden:strict-config-decoder") | .severity // ""')
	sev_sib=$(echo "$FLOWS_JSON" | jq -r --arg f "$flow_id" '.flows[] | select(.id==$f) | .rules[] | select(.id=="golden:sibling-export-hazard") | .severity // ""')
	sev_ui=$(echo "$FLOWS_JSON" | jq -r --arg f "$flow_id" '.flows[] | select(.id==$f) | .rules[] | select(.id=="golden:ui-budget-and-spec-floor") | .severity // ""')
	sev_ti=$(echo "$FLOWS_JSON" | jq -r --arg f "$flow_id" '.flows[] | select(.id==$f) | .rules[] | select(.id=="golden:test-integrity-guard") | .severity // "blocking"')
	assert_eq "$flow_id strict-config-decoder severity" "$sev_dec" "warn"
	assert_eq "$flow_id sibling-export-hazard severity" "$sev_sib" "warn"
	assert_eq "$flow_id ui-budget-and-spec-floor severity" "$sev_ui" "warn"
	assert_eq "$flow_id test-integrity-guard severity" "$sev_ti" "blocking"
done

# Configure RBAC allowlist & custom scope set for Item 4 refusal matrix.
api PUT /api/api-scope-sets '{"set":{"name":"walk/widgets-merger","description":"merge:write and tasks:write on acme/widgets only","scopes":["tasks:read","tasks:write","merge:write"],"repos":["acme/widgets"]}}' >"$WORK_DIR/captures/scopeset.json"
api POST /api/access/principals '{"entry":"user:alice@example.com","role":"grain/operator","reason":"deployment owner"}' >/dev/null
api POST /api/access/principals '{"entry":"delegate:slack-bot@example.iam.gserviceaccount.com","role":"grain/delegated","reason":"delegation walkthrough","label":"slack-bot"}' >/dev/null
api POST /api/access/principals '{"entry":"user:viewer@example.com","role":"grain/viewer","reason":"viewer role"}' >/dev/null
api POST /api/access/principals '{"entry":"user:contrib@example.com","role":"grain/contributor","reason":"contributor role"}' >/dev/null
api POST /api/access/principals '{"entry":"user:widgets-merger@example.com","role":"walk/widgets-merger","reason":"widgets-only merger"}' >/dev/null
EXT_AGENT_TOK=$(api POST /api/external-agents/tokens '{"name":"walk-agent-merger","owner":"alice@example.com","scopes":["tasks:read","tasks:write","merge:write"]}' | jq -r '.token')
if [[ -n "$EXT_AGENT_TOK" && "$EXT_AGENT_TOK" != "null" ]]; then
	ok "Minted external agent token with merge:write"
else
	fail "Failed to mint external agent token"
fi

# ============================================================================
# ITEM 1: Shallow-clone git merge-base S3 resolution, missing-base diagnostics,
# and URL credential redaction.
# ============================================================================
printf "\n--- Item 1: Shallow-clone git merge-base, missing base, credential redaction ---\n"

# 1a-controller: Dispatch T1 so it gets a genuine treatment QA assignment,
# park without running in-sandbox S3, push a staged branch forked from FORK_SHA
# (70 commits behind main), and invoke POST /api/tasks/{id}/pull-request to
# exercise controller MirrorWorkspace (--depth=64 -> --deepen=256 -> FORK_SHA).
T1=$(api POST /api/tasks '{"title":"Diverged shallow merge-base check","description":"Add clean helper forked 70 commits behind main","repo":"acme/widgets","base":"main","approved":false}' | jq -r '.id')
cat >"$WORK_DIR/action-$T1.json" <<'EOF'
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "ask_question",
      "args": {
        "summary": "Enrolled in treatment arm; ready for controller mirror S3 check",
        "question": "Ready for controller-side S3 mirror evaluation."
      }
    }
  ],
  "response": "Parked for controller mirror S3."
}
EOF
api POST "/api/tasks/$T1/approve" '{}' >/dev/null
wait_task_state "$T1" "awaiting_reply" 120 >/dev/null || fail "1a task $T1 did not park"
T1_STAGE="grain-qa-stage/$T1"
T1_SHA=$(push_commit_to_bare "$WORK_DIR/widgets.git" "fork-point" "$T1_STAGE" "pkg/feature/clean.go" $'package feature\n\nfunc Clean() int { return 42 }\n' $'feat: add clean helper\n\nForked from C1.')
T1_PR=$(api POST "/api/tasks/$T1/pull-request" '{}')
echo "$T1_PR" >"$WORK_DIR/captures/t1-pr.json"
T1_QA=$(api GET "/api/tasks/$T1/qa")
echo "$T1_QA" >"$WORK_DIR/captures/t1-qa.json"
T1_BASE_SHA=$(echo "$T1_QA" | jq -r '[.checkRuns[] | select(.stage=="S3" and (.baseSha // "") != "")][0].baseSha // ""')
assert_eq "1a controller mirror S3 resolved exact merge-base (FORK_SHA, not ROOT_SHA/MAIN_SHA)" "$T1_BASE_SHA" "$FORK_SHA"
if [[ "$T1_BASE_SHA" != "$ROOT_SHA" && "$T1_BASE_SHA" != "$MAIN_SHA" ]]; then
	ok "1a baseSha differs from ROOT_SHA and MAIN_SHA"
else
	fail "1a baseSha wrongly matched ROOT_SHA or MAIN_SHA"
fi
T1_PR_NUM=$(echo "$T1_PR" | jq -r '.number // 0')
T1_PR_NOTE=$(echo "$T1_PR" | jq -r '.note // ""')
if [[ "$T1_PR_NUM" -gt 0 ]]; then
	ok "1a promoted staging branch and opened PR #$T1_PR_NUM"
elif [[ "$T1_PR_NOTE" == *"S3) passed on staging ref"* ]]; then
	ok "1a S3 passed on staging ref and deferred to S4 turn-end promotion"
	# Finish T1's turn so salvage runs S4 and promotes grain-qa-stage/$T1 to PR!
	cat >"$WORK_DIR/action-$T1.json" <<'EOF'
{"kind":"plain_reply","response":"Ending turn so S4 promotes staging branch."}
EOF
	api POST "/api/tasks/$T1/reply" '{"body":"End turn to promote"}' >/dev/null
	T1_FINAL_PR=$(wait_task_pr "$T1" 90 || true)
	if [[ -n "$T1_FINAL_PR" ]]; then
		ok "1a promoted staging branch after turn end and opened $T1_FINAL_PR"
	else
		fail "1a did not open PR after S4 salvage"
	fi
else
	fail "1a expected PR or S4 deferral note, got: $T1_PR"
fi

# 1a-sandbox: Real sandbox dispatch with depth=1 shallow checkout forked from FORK_SHA.
T1S=$(api POST /api/tasks '{"title":"Sandbox depth-1 shallow merge-base","description":"Verify SandboxWorkspace deepens depth=1 clone to exact merge-base","repo":"acme/widgets","base":"main","approved":false}' | jq -r '.id')
cat >"$WORK_DIR/action-$T1S.json" <<EOF
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "description": "re-clone shallow depth=1 at fork-point and push clean commit to staging ref",
        "command": "set -euo pipefail; cd work; ORIGIN=\$(git remote get-url origin); BR=\$(git branch --show-current); cd ..; rm -rf work; git clone --quiet --depth=1 --branch fork-point \"\$ORIGIN\" work; cd work; git checkout -q -b \"\$BR\"; mkdir -p pkg/sbox; printf 'package sbox\\n\\nfunc OK() int { return 7 }\\n' > pkg/sbox/ok.go; git add pkg/sbox/ok.go; git -c user.name='Walk Agent' -c user.email='agent@example.com' commit -qm 'feat: sandbox shallow commit'; git push -q origin HEAD:refs/heads/grain-qa-stage/$T1S; grain pr open"
      }
    }
  ],
  "response": "Opened PR from depth=1 shallow sandbox checkout."
}
EOF
api POST "/api/tasks/$T1S/approve" '{}' >/dev/null
wait_task_attempt_done "$T1S" 1 120 || fail "1a-sandbox task $T1S did not finish attempt 1"
T1S_PR=$(wait_task_pr "$T1S" 60 || true)
T1S_QA=$(api GET "/api/tasks/$T1S/qa")
echo "$T1S_QA" >"$WORK_DIR/captures/t1s-qa.json"
T1S_BASE_SHA=$(echo "$T1S_QA" | jq -r '[.checkRuns[] | select(.stage=="S3" and (.baseSha // "") != "")][0].baseSha // ""')
assert_eq "1a-sandbox depth=1 checkout deepened and resolved exact merge-base FORK_SHA" "$T1S_BASE_SHA" "$FORK_SHA"
if [[ -n "$T1S_PR" ]]; then
	ok "1a-sandbox promoted and opened $T1S_PR"
else
	fail "1a-sandbox did not open PR; mcp log: $(cat "$WORK_DIR/runs/mcp-$T1S.jsonl" 2>/dev/null || true)"
fi

# 1b: Missing/deleted base branch -> checks.ErrBaseResolutionFailed + persisted qa:base-resolution check run.
T2=$(api POST /api/tasks '{"title":"Deleted base branch diagnostic","description":"Verify ErrBaseResolutionFailed and qa:base-resolution check run","repo":"acme/widgets","base":"temp-base","approved":false}' | jq -r '.id')
cat >"$WORK_DIR/action-$T2.json" <<'EOF'
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "ask_question",
      "args": {
        "summary": "Enrolled for deleted base branch S3 check",
        "question": "Ready for deleted base branch mirror S3 evaluation."
      }
    }
  ],
  "response": "Parked for deleted base check."
}
EOF
api POST "/api/tasks/$T2/approve" '{}' >/dev/null
wait_task_state "$T2" "awaiting_reply" 120 >/dev/null || fail "1b task $T2 did not park"
T2_STAGE="grain-qa-stage/$T2"
T2_SHA=$(push_commit_to_bare "$WORK_DIR/widgets.git" "temp-base" "$T2_STAGE" "pkg/feature/deleted_base.go" $'package feature\n\nfunc DeletedBase() int { return 1 }\n' $'feat: commit on temp-base\n\nBody.')
# Delete temp-base from bare repo so S3 cannot resolve merge-base!
git -C "$WORK_DIR/widgets.git" update-ref -d refs/heads/temp-base
T2_CODE=$(http_status_and_body "$WORK_DIR/captures/t2-pr-err.txt" -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d '{}' "$DAEMON/api/tasks/$T2/pull-request")
T2_BODY=$(cat "$WORK_DIR/captures/t2-pr-err.txt")
if [[ "$T2_CODE" -ge 400 ]] && [[ "$T2_BODY" == *"failed to resolve base commit"* || "$T2_BODY" == *"base resolution failed"* ]]; then
	ok "1b missing base branch rejected ($T2_CODE): $T2_BODY"
else
	fail "1b expected ErrBaseResolutionFailed error, got HTTP $T2_CODE: $T2_BODY"
fi
T2_QA=$(api GET "/api/tasks/$T2/qa")
echo "$T2_QA" >"$WORK_DIR/captures/t2-qa.json"
T2_CHK=$(echo "$T2_QA" | jq -r '[.checkRuns[] | select(.checkId=="qa:base-resolution" and .verdict=="error" and (.baseSha // "") == "")] | length')
if [[ "$T2_CHK" -ge 1 ]]; then
	ok "1b persisted diagnostic qa:base-resolution check run (verdict=error, baseSha='')"
else
	fail "1b missing qa:base-resolution check run: $T2_QA"
fi
T2_S3_BLOCKED=$(echo "$T2_QA" | jq -r '.s3Blocked')
assert_eq "1b task reports s3Blocked=true after qa:base-resolution error" "$T2_S3_BLOCKED" "true"

# 1b-salvage: Also verify what happens when an agent finishes a turn without calling
# `grain pr open` while its target base branch is deleted (salvageQAStageIfPresent path).
T2S=$(api POST /api/tasks '{"title":"Salvage deleted base branch","description":"Check salvageQAStageIfPresent records qa:base-resolution on ErrBaseResolutionFailed","repo":"acme/widgets","base":"temp-base-salvage","approved":false}' | jq -r '.id')
cat >"$WORK_DIR/action-$T2S.json" <<EOF
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "description": "push staged commit then delete temp-base-salvage before salvage",
        "command": "set -euo pipefail; cd work; mkdir -p pkg/salv; printf 'package salv\\nfunc S() int { return 1 }\\n' > pkg/salv/s.go; git add pkg/salv/s.go; git -c user.name='Walk Agent' -c user.email='agent@example.com' commit -qm 'feat: salvage deleted base'; git push -q origin HEAD:refs/heads/grain-qa-stage/$T2S; git --git-dir='$WORK_DIR/widgets.git' update-ref -d refs/heads/temp-base-salvage"
      }
    }
  ],
  "response": "Pushed commit without calling open_pull_request so salvage runs."
}
EOF
api POST "/api/tasks/$T2S/approve" '{}' >/dev/null
wait_task_attempt_done "$T2S" 1 120 || fail "1b-salvage task $T2S did not finish attempt 1"
sleep 1.5
T2S_QA=$(api GET "/api/tasks/$T2S/qa")
echo "$T2S_QA" >"$WORK_DIR/captures/t2s-qa.json"
T2S_CHK=$(echo "$T2S_QA" | jq -r '[.checkRuns[] | select(.checkId=="qa:base-resolution" and .verdict=="error")] | length')
if [[ "$T2S_CHK" -ge 1 ]]; then
	ok "1b-salvage persisted qa:base-resolution check run during salvage"
else
	fail "1b-salvage DID NOT persist qa:base-resolution check run on ErrBaseResolutionFailed (salvageQAStageIfPresent gap!)"
fi

# 1c: Embedded userinfo URL credential redaction & git argv verification.
# Create a clean staging branch forked from FORK_SHA for cred_probe.
CRED_BRANCH="grain-qa-stage/cred-probe"
push_commit_to_bare "$WORK_DIR/widgets.git" "fork-point" "$CRED_BRANCH" "pkg/feature/clean.go" $'package feature\n\nfunc Clean() int { return 99 }\n' $'feat: cred probe commit\n\nBody.' >/dev/null
WRAP_DIR="$WORK_DIR/git-wrap"
mkdir -p "$WRAP_DIR"
REAL_GIT=$(command -v git)
ARGV_LOG="$WORK_DIR/captures/git-argv.log"
: >"$ARGV_LOG"
cat >"$WRAP_DIR/git" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$ARGV_LOG"
exec "$REAL_GIT" "\$@"
EOF
chmod +x "$WRAP_DIR/git"
CRED_OUT=$(PATH="$WRAP_DIR:$PATH" "$WORK_DIR/bin/cred_probe" \
	-remote-url "http://botuser:s3cr3t-w4lk-t0k3n@${GHSIM}/acme/widgets.git" \
	-base main \
	-head "$CRED_BRANCH" \
	-missing-base "deleted-base-branch" \
	-secret "s3cr3t-w4lk-t0k3n" \
	-cache-dir "$WORK_DIR/cred-cache" \
	-argv-log "$ARGV_LOG")
echo "$CRED_OUT" >"$WORK_DIR/captures/cred-probe.json"
assert_eq "1c cred_probe baseSha == FORK_SHA" "$(echo "$CRED_OUT" | jq -r '.baseSha')" "$FORK_SHA"
assert_eq "1c cred_probe deepened shallow fetch" "$(echo "$CRED_OUT" | jq -r '.deepenInvoked')" "true"
assert_eq "1c cred_probe diff has task file only" "$(echo "$CRED_OUT" | jq -r '.diffHasCleanGo')/$(echo "$CRED_OUT" | jq -r '.diffHasMainOnly')" "true/false"
assert_eq "1c cred_probe missing base returns ErrBaseResolutionFailed" "$(echo "$CRED_OUT" | jq -r '.isBaseResErr')" "true"
assert_eq "1c cred_probe zero credential leaks in git argv or errors" "$(echo "$CRED_OUT" | jq -r '.leaks | length')" "0"
if grep -q "s3cr3t-w4lk-t0k3n\|ghp_walkthrough" "$WORK_DIR/daemon.log" "$WORK_DIR/captures/t2-qa.json"; then
	fail "1c plaintext credential found in daemon.log or t2-qa.json"
else
	ok "1c zero plaintext credentials in daemon.log or persisted qa:base-resolution check runs"
fi

# ============================================================================
# ITEM 2: Rule severity enforcement end to end (warn permits promotion; blocking blocks)
# ============================================================================
printf "\n--- Item 2: QAEngineRev=7 rule severities (warn vs blocking) ---\n"

# 2a: Warning-only violation (golden:strict-config-decoder) -> records warn finding yet promotes & opens PR!
T3=$(api POST /api/tasks '{"title":"Warning-only strict config decoder","description":"Add config decoder without DisallowUnknownFields to verify warn-only promotion","repo":"acme/widgets","base":"main","approved":false}' | jq -r '.id')
cat >"$WORK_DIR/action-$T3.json" <<EOF
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "description": "commit warning-only strict-config-decoder violation and open PR",
        "command": "set -euo pipefail; cd work; mkdir -p pkg/config; cat > pkg/config/loader.go <<'GO'\\npackage config\\n\\nimport (\\n\\t\"encoding/json\"\\n\\t\"io\"\\n)\\n\\ntype AppConfig struct {\\n\\tPort int \`json:\"port\"\`\\n}\\n\\nfunc Load(r io.Reader) (AppConfig, error) {\\n\\tvar cfg AppConfig\\n\\terr := json.NewDecoder(r).Decode(&cfg)\\n\\treturn cfg, err\\n}\\nGO\\ngit add pkg/config/loader.go; git -c user.name='Walk Agent' -c user.email='agent@example.com' commit -qm 'feat(config): add loader'; git push -q origin HEAD:refs/heads/grain-qa-stage/$T3; grain pr open"
      }
    }
  ],
  "response": "Pushed strict-config-decoder change and opened PR."
}
EOF
api POST "/api/tasks/$T3/approve" '{}' >/dev/null
wait_task_attempt_done "$T3" 1 120 || fail "2a task $T3 did not finish attempt 1"
T3_PR=$(wait_task_pr "$T3" 60 || true)
T3_DETAIL=$(api GET "/api/tasks/$T3")
T3_QA=$(api GET "/api/tasks/$T3/qa")
echo "$T3_QA" >"$WORK_DIR/captures/t3-qa.json"
T3_WARN=$(echo "$T3_QA" | jq -r '[.findings[] | select(.ruleId=="golden:strict-config-decoder" and .severity=="warn")] | length')
if [[ "$T3_WARN" -ge 1 ]]; then
	ok "2a golden:strict-config-decoder recorded severity=warn finding(s) ($T3_WARN)"
else
	fail "2a expected golden:strict-config-decoder warn finding, got: $(echo "$T3_QA" | jq -c '.findings')"
fi
if [[ -n "$T3_PR" ]]; then
	ok "2a warning-only S3 permitted promotion and opened $T3_PR"
else
	fail "2a warning-only S3 blocked PR creation! detail: $(echo "$T3_DETAIL" | jq -c '{state, pullRequest}')"
fi

# 2b: Blocking violation (golden:test-integrity-guard) -> blocks promotion & holds on grain-qa-stage/<id>!
T4=$(api POST /api/tasks '{"title":"Blocking test-integrity-guard check","description":"Push assertion-free test to verify S3 blocks promotion","repo":"acme/widgets","base":"main","approved":false}' | jq -r '.id')
cat >"$WORK_DIR/action-$T4.json" <<EOF
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "description": "push assertion-free test and try grain pr open (expect S3 block)",
        "command": "set -euo pipefail; cd work; mkdir -p pkg/broken; cat > pkg/broken/empty_test.go <<'GO'\\npackage broken\\n\\nimport \"testing\"\\n\\nfunc TestNothing(t *testing.T) {}\\nGO\\ngit add pkg/broken/empty_test.go; git -c user.name='Walk Agent' -c user.email='agent@example.com' commit -qm 'test: empty test'; git push -q origin HEAD:refs/heads/grain-qa-stage/$T4; if grain pr open > /tmp/t4-open.out 2>&1; then echo UNEXPECTED_OPEN; cat /tmp/t4-open.out; exit 1; else echo BLOCKED_AS_EXPECTED; cat /tmp/t4-open.out; fi"
      }
    },
    {
      "tool": "ask_question",
      "args": {
        "summary": "S3 blocked promotion; awaiting operator review",
        "question": "S3 test-integrity-guard blocked PR promotion on the staging branch."
      }
    }
  ],
  "response": "S3 blocked PR promotion as expected."
}
EOF
api POST "/api/tasks/$T4/approve" '{}' >/dev/null
wait_task_state "$T4" "awaiting_reply" 120 >/dev/null || fail "2b task $T4 did not reach awaiting_reply"
T4_DETAIL=$(api GET "/api/tasks/$T4")
T4_QA=$(api GET "/api/tasks/$T4/qa")
echo "$T4_QA" >"$WORK_DIR/captures/t4-qa.json"
T4_BRANCH=$(echo "$T4_DETAIL" | jq -r '.branch')
T4_PR=$(echo "$T4_DETAIL" | jq -r '.pullRequest // ""')
assert_eq "2b s3Blocked is true" "$(echo "$T4_QA" | jq -r '.s3Blocked')" "true"
assert_eq "2b stageBranch is grain-qa-stage/$T4" "$(echo "$T4_QA" | jq -r '.stageBranch')" "grain-qa-stage/$T4"
assert_eq "2b no PR opened while S3 blocked" "$T4_PR" ""
BRANCHES_JSON=$(curl -fsS "http://$GHSIM/_sim/branches")
if echo "$BRANCHES_JSON" | jq -e --arg b "grain-qa-stage/$T4" 'has($b)' >/dev/null && ! echo "$BRANCHES_JSON" | jq -e --arg b "$T4_BRANCH" 'has($b)' >/dev/null; then
	ok "2b commits held on grain-qa-stage/$T4 and $T4_BRANCH not created"
else
	fail "2b unexpected remote branches: $BRANCHES_JSON"
fi

# ============================================================================
# ITEM 3: Staging CI ref propagation (grain-qa-stage/<id>) for Grain CI &
# GitHub Actions (ExternalCI), then switch to task.BranchName() after promotion
# ============================================================================
printf "\n--- Item 3: Staging CI ref propagation (Grain CI + GitHub Actions) ---\n"

# 3a: Grain CI mode on acme/widgets
api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"grain"}' >"$WORK_DIR/captures/cimode-grain.json"
T5=$(api POST /api/tasks '{"title":"Staging CI Grain CI mode","description":"Verify grain ci wait, list, pr status target grain-qa-stage/<id> then switch on promotion","repo":"acme/widgets","base":"main","approved":false,"grants":[{"capability":"https://grain.dev/capabilities/api#ci-read","with":{"repo":"acme/widgets"}}]}' | jq -r '.id')
cat >"$WORK_DIR/action-$T5.json" <<EOF
[
  {
    "kind": "mcp_sequence",
    "calls": [
      {
        "tool": "run_command",
        "args": {
          "description": "push S3-blocked commit to staging and run grain ci wait/list/pr status",
          "command": "set -euo pipefail; cd work; mkdir -p pkg/stgci; printf 'package stgci\\nimport \"testing\"\\nfunc TestEmpty(t *testing.T) {}\\n' > pkg/stgci/empty_test.go; git add pkg/stgci/empty_test.go; git -c user.name='Walk Agent' -c user.email='agent@example.com' commit -qm 'test: staged grain ci'; git push -q origin HEAD:refs/heads/grain-qa-stage/$T5; grain pr open || true; (grain ci wait > '$WORK_DIR/captures/t5-ci-wait-stage.txt' 2>&1 || true); grain ci list > '$WORK_DIR/captures/t5-ci-list-stage.txt' 2>&1; grain pr status > '$WORK_DIR/captures/t5-pr-status-stage.txt' 2>&1"
        }
      },
      {
        "tool": "ask_question",
        "args": {
          "summary": "Waiting for operator to promote staging ref for post-promotion check",
          "question": "Grain CI verified on staging ref."
        }
      }
    ],
    "response": "Checked Grain CI on staging ref."
  },
  {
    "kind": "mcp_sequence",
    "calls": [
      {
        "tool": "run_command",
        "args": {
          "description": "verify grain ci wait/list/pr status switched to promoted task branch",
          "command": "set -euo pipefail; (grain ci wait > '$WORK_DIR/captures/t5-ci-wait-promoted.txt' 2>&1 || true); grain ci list > '$WORK_DIR/captures/t5-ci-list-promoted.txt' 2>&1; grain pr status > '$WORK_DIR/captures/t5-pr-status-promoted.txt' 2>&1"
        }
      },
      {
        "tool": "comment_on_issue",
        "args": {
          "comment": "Post-promotion Grain CI switched cleanly."
        }
      }
    ],
    "response": "Checked Grain CI after promotion."
  }
]
EOF
api POST "/api/tasks/$T5/approve" '{}' >/dev/null
wait_task_state "$T5" "awaiting_reply" 120 >/dev/null || fail "3a task $T5 did not reach awaiting_reply"
T5_BRANCH=$(api GET "/api/tasks/$T5" | jq -r '.branch')
T5_STAGE_SHA=$(curl -fsS "http://$GHSIM/_sim/branches" | jq -r --arg b "grain-qa-stage/$T5" '.[$b]')
assert_contains "3a sandbox grain ci wait targeted commit on grain-qa-stage/$T5" "$(cat "$WORK_DIR/captures/t5-ci-wait-stage.txt" 2>/dev/null || true)" "${T5_STAGE_SHA:0:12}"
assert_contains "3a sandbox grain ci list shows grain-qa-stage/$T5" "$(cat "$WORK_DIR/captures/t5-ci-list-stage.txt" 2>/dev/null || true)" "grain-qa-stage/$T5"
assert_contains "3a sandbox grain pr status targets grain-qa-stage/$T5" "$(cat "$WORK_DIR/captures/t5-pr-status-stage.txt" 2>/dev/null || true)" "grain-qa-stage/$T5"
T5_API_RUNS=$(api GET "/api/ci/runs?repo=acme/widgets&ref=grain-qa-stage/$T5")
echo "$T5_API_RUNS" >"$WORK_DIR/captures/t5-api-runs-stage.json"
if [[ $(echo "$T5_API_RUNS" | jq '.runs | length') -ge 1 ]]; then
	ok "3a API GET /api/ci/runs lists Grain CI run on grain-qa-stage/$T5"
else
	fail "3a API GET /api/ci/runs returned no runs on grain-qa-stage/$T5"
fi
# Also verify POST /api/ci/runs (taskId scoped re-run) targets grain-qa-stage/$T5!
T5_RERUN=$(api POST /api/ci/runs "{\"taskId\":\"$T5\",\"recheck\":true}")
assert_eq "3a API task-scoped Grain CI recheck targets grain-qa-stage/$T5" "$(echo "$T5_RERUN" | jq -r '.ref')" "grain-qa-stage/$T5"

# Now bypass S3 on T5 so grain-qa-stage/$T5 promotes to $T5_BRANCH and is deleted,
# push a newer commit onto $T5_BRANCH, and verify sandbox CI switches to $T5_BRANCH!
api POST "/api/tasks/$T5/qa/bypass-s3" '{"reason":"Promote to test post-promotion Grain CI ref switch"}' >"$WORK_DIR/captures/t5-bypass.json"
push_commit_to_bare "$WORK_DIR/widgets.git" "$T5_BRANCH" "$T5_BRANCH" "pkg/stgci/post_promo_test.go" $'package stgci\nimport "testing"\nfunc TestPostPromo(t *testing.T) { if 1 != 1 { t.Fatal() } }\n' $'test: post-promotion commit on task branch\n\nBody.' >/dev/null
api POST "/api/tasks/$T5/reply" '{"body":"Promoted; verify CI switches to task branch"}' >/dev/null
wait_task_attempt_done "$T5" 2 120 || fail "3a task $T5 attempt 2 did not finish"
assert_contains "3a post-promotion grain pr status switched to $T5_BRANCH" "$(cat "$WORK_DIR/captures/t5-pr-status-promoted.txt" 2>/dev/null || true)" "$T5_BRANCH"
assert_contains "3a post-promotion grain ci list includes $T5_BRANCH" "$(cat "$WORK_DIR/captures/t5-ci-list-promoted.txt" 2>/dev/null || true)" "$T5_BRANCH"

# 3b: GitHub Actions / ExternalCI mode on acme/widgets
api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"github"}' >"$WORK_DIR/captures/cimode-github.json"
T6=$(api POST /api/tasks '{"title":"Staging CI GitHub Actions mode","description":"Verify ExternalCI on grain-qa-stage/<id>, Actions rerun, and post-promotion switch","repo":"acme/widgets","base":"main","approved":false,"grants":[{"capability":"https://grain.dev/capabilities/api#ci-read","with":{"repo":"acme/widgets"}}]}' | jq -r '.id')
cat >"$WORK_DIR/action-$T6.json" <<EOF
[
  {
    "kind": "mcp_sequence",
    "calls": [
      {
        "tool": "run_command",
        "args": {
          "description": "push S3-blocked commit to staging branch for Actions test",
          "command": "set -euo pipefail; cd work; mkdir -p pkg/gha; printf 'package gha\\nimport \"testing\"\\nfunc TestBlank(t *testing.T) {}\\n' > pkg/gha/blank_test.go; git add pkg/gha/blank_test.go; git -c user.name='Walk Agent' -c user.email='agent@example.com' commit -qm 'test: staged actions commit'; git push -q origin HEAD:refs/heads/grain-qa-stage/$T6; grain pr open || true"
        }
      },
      {
        "tool": "ask_question",
        "args": {
          "summary": "Seed GitHub Actions run on staging ref",
          "question": "Ready for Actions run seeding on grain-qa-stage/$T6."
        }
      }
    ],
    "response": "Staged commit pushed."
  },
  {
    "kind": "mcp_sequence",
    "calls": [
      {
        "tool": "run_command",
        "args": {
          "description": "inspect and retrigger seeded GitHub Actions run on grain-qa-stage/<id>",
          "command": "set -euo pipefail; grain ci list > '$WORK_DIR/captures/t6-ci-list-stage.txt' 2>&1; grain pr status > '$WORK_DIR/captures/t6-pr-status-stage.txt' 2>&1; (grain ci wait > '$WORK_DIR/captures/t6-ci-wait-stage.txt' 2>&1 || true); grain ci retrigger > '$WORK_DIR/captures/t6-ci-retrigger-stage.txt' 2>&1"
        }
      },
      {
        "tool": "ask_question",
        "args": {
          "summary": "Ready for S3 bypass promotion and post-promotion Actions check",
          "question": "Inspected and retriggered staging Actions runs."
        }
      }
    ],
    "response": "Inspected staging Actions runs."
  },
  {
    "kind": "mcp_sequence",
    "calls": [
      {
        "tool": "run_command",
        "args": {
          "description": "inspect GitHub Actions run on promoted task branch",
          "command": "set -euo pipefail; grain ci list > '$WORK_DIR/captures/t6-ci-list-promoted.txt' 2>&1; grain pr status > '$WORK_DIR/captures/t6-pr-status-promoted.txt' 2>&1; (grain ci wait > '$WORK_DIR/captures/t6-ci-wait-promoted.txt' 2>&1 || true)"
        }
      },
      {
        "tool": "comment_on_issue",
        "args": {
          "comment": "Verified ExternalCI switch after promotion."
        }
      }
    ],
    "response": "Verified ExternalCI switch."
  }
]
EOF
api POST "/api/tasks/$T6/approve" '{}' >/dev/null
wait_task_state "$T6" "awaiting_reply" 120 >/dev/null || fail "3b task $T6 attempt 1 did not park"
T6_BRANCH=$(api GET "/api/tasks/$T6" | jq -r '.branch')
T6_STAGE="grain-qa-stage/$T6"
T6_STAGE_SHA=$(curl -fsS "http://$GHSIM/_sim/branches" | jq -r --arg b "$T6_STAGE" '.[$b]')
NOW_RFC=$(date -u +%Y-%m-%dT%H:%M:%SZ)
curl -fsS -X POST -H "Content-Type: application/json" \
	-d "{\"runs\":[{\"id\":91001,\"name\":\"CI\",\"path\":\".github/workflows/ci.yml\",\"headBranch\":\"$T6_STAGE\",\"headSha\":\"$T6_STAGE_SHA\",\"event\":\"push\",\"status\":\"completed\",\"conclusion\":\"failure\",\"createdAt\":\"$NOW_RFC\",\"updatedAt\":\"$NOW_RFC\",\"jobs\":[{\"id\":910011,\"name\":\"test\",\"status\":\"completed\",\"conclusion\":\"failure\",\"log\":\"FAIL TestBlank\"}]}]}" \
	"http://$GHSIM/_sim/seed-actions" >/dev/null
curl -fsS -X POST -H "Content-Type: application/json" \
	-d "{\"ref\":\"$T6_STAGE_SHA\",\"checks\":[{\"id\":910011,\"name\":\"CI / test\",\"status\":\"completed\",\"conclusion\":\"failure\"}]}" \
	"http://$GHSIM/_sim/check-runs" >/dev/null

# Verify API GET /api/ci/runs (with provider=github-actions) and POST /api/ci/runs/gha:acme:widgets:91001/rerun target grain-qa-stage/$T6!
T6_API_RUNS=$(api GET "/api/ci/runs?repo=acme/widgets&ref=$T6_STAGE&provider=github-actions")
assert_contains "3b API GET /api/ci/runs?provider=github-actions returns Actions run 91001 on $T6_STAGE" "$T6_API_RUNS" "91001"
T6_RERUN=$(api POST "/api/ci/runs/gha:acme:widgets:91001/rerun" '{}')
echo "$T6_RERUN" >"$WORK_DIR/captures/t6-actions-rerun-stage.json"
assert_eq "3b POST /api/ci/runs/gha:acme:widgets:91001/rerun targets $T6_STAGE" "$(echo "$T6_RERUN" | jq -r '.branch')" "$T6_STAGE"
SIM_RERUNS=$(curl -fsS "http://$GHSIM/_sim/reruns")
assert_contains "3b ghsim recorded RerunFailedJobs for run 91001" "$SIM_RERUNS" "91001"

# Let attempt 2 inspect & retrigger staging Actions via sandbox grain ci list / pr status / ci wait / ci retrigger
api POST "/api/tasks/$T6/reply" '{"body":"Actions run seeded on staging ref; inspect and retrigger"}' >/dev/null
wait_task_attempt_done "$T6" 2 120 || fail "3b task $T6 attempt 2 did not finish"
wait_task_state "$T6" "awaiting_reply" 60 >/dev/null || true
assert_contains "3b sandbox grain ci list sees Actions run on $T6_STAGE" "$(cat "$WORK_DIR/captures/t6-ci-list-stage.txt" 2>/dev/null || true)" "$T6_STAGE"
assert_contains "3b sandbox grain pr status reports Actions run 91001 on $T6_STAGE" "$(cat "$WORK_DIR/captures/t6-pr-status-stage.txt" 2>/dev/null || true)" "91001"
assert_contains "3b sandbox grain ci wait sees failed staging check on ${T6_STAGE_SHA:0:12}" "$(cat "$WORK_DIR/captures/t6-ci-wait-stage.txt" 2>/dev/null || true)" "CI / test"
assert_contains "3b sandbox grain ci retrigger re-ran staging Actions run 91001" "$(cat "$WORK_DIR/captures/t6-ci-retrigger-stage.txt" 2>/dev/null || true)" "91001"

# Promote T6 via bypass-s3, push commit 2 to $T6_BRANCH, seed passing Actions run + check run on $T6_BRANCH, verify switch!
api POST "/api/tasks/$T6/qa/bypass-s3" '{"reason":"Promote T6 to test ExternalCI switch to task branch"}' >"$WORK_DIR/captures/t6-bypass.json"
T6_PROMOTED_SHA=$(push_commit_to_bare "$WORK_DIR/widgets.git" "$T6_BRANCH" "$T6_BRANCH" "pkg/gha/fixed_test.go" $'package gha\nimport "testing"\nfunc TestFixed(t *testing.T) { if 1 != 1 { t.Fatal() } }\n' $'test: post-promotion commit\n\nBody.')
LATER_RFC=$(date -u -d "+5 seconds" +%Y-%m-%dT%H:%M:%SZ)
curl -fsS -X POST -H "Content-Type: application/json" \
	-d "{\"append\":true,\"runs\":[{\"id\":91002,\"name\":\"CI\",\"path\":\".github/workflows/ci.yml\",\"headBranch\":\"$T6_BRANCH\",\"headSha\":\"$T6_PROMOTED_SHA\",\"event\":\"push\",\"status\":\"completed\",\"conclusion\":\"success\",\"createdAt\":\"$LATER_RFC\",\"updatedAt\":\"$LATER_RFC\",\"jobs\":[{\"id\":910021,\"name\":\"test\",\"status\":\"completed\",\"conclusion\":\"success\",\"log\":\"PASS\"}]}]}" \
	"http://$GHSIM/_sim/seed-actions" >/dev/null
curl -fsS -X POST -H "Content-Type: application/json" \
	-d "{\"ref\":\"$T6_PROMOTED_SHA\",\"checks\":[{\"id\":910021,\"name\":\"CI / test\",\"status\":\"completed\",\"conclusion\":\"success\"}]}" \
	"http://$GHSIM/_sim/check-runs" >/dev/null
api POST "/api/tasks/$T6/reply" '{"body":"Promoted and new Actions run seeded on task branch; inspect it"}' >/dev/null
wait_task_attempt_done "$T6" 3 120 || fail "3b task $T6 attempt 3 did not finish"
assert_contains "3b post-promotion grain pr status reports Actions run 91002 on $T6_BRANCH" "$(cat "$WORK_DIR/captures/t6-pr-status-promoted.txt" 2>/dev/null || true)" "91002"
assert_contains "3b post-promotion grain ci list shows run 91002 on $T6_BRANCH" "$(cat "$WORK_DIR/captures/t6-ci-list-promoted.txt" 2>/dev/null || true)" "91002"
assert_contains "3b post-promotion grain ci wait reads passing check on $T6_BRANCH" "$(cat "$WORK_DIR/captures/t6-ci-wait-promoted.txt" 2>/dev/null || true)" "CI / test"

# ============================================================================
# ITEM 4: Human S3 bypass full authorization, safety, and concurrency matrix
# ============================================================================
printf "\n--- Item 4: Human S3 bypass authorization, validation & concurrency matrix ---\n"

# Prepare Task T7 (autoMerge: true, secondary write repo acme/secondary, S3-blocked on staging)
# and Task T7b (autoMerge: false, secondary write repo acme/secondary, S3-blocked on staging)
# via genuine sandbox dispatch so each holds a real treatment QA assignment.
prepare_blocked_task() {
	local title=$1 body=$2 auto_merge=$3 writes_json=$4 file_rel=$5 func_name=$6
	local tid
	tid=$(api POST /api/tasks "{\"title\":\"$title\",\"description\":\"$body\",\"repo\":\"acme/widgets\",\"base\":\"main\",\"autoMerge\":$auto_merge,\"writes\":$writes_json,\"approved\":false}" | jq -r '.id')
	cat >"$WORK_DIR/action-$tid.json" <<EOF
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "description": "push assertion-free test to staging and trigger S3 block",
        "command": "set -euo pipefail; cd work; mkdir -p \$(dirname '$file_rel'); printf 'package bypass\\n\\nimport \"testing\"\\n\\nfunc $func_name(t *testing.T) {}\\n' > '$file_rel'; git add '$file_rel'; git -c user.name='Walk Agent' -c user.email='agent@example.com' commit -qm 'test: trigger test-integrity-guard'; git push -q origin HEAD:refs/heads/grain-qa-stage/$tid; grain pr open || true"
      }
    },
    {
      "tool": "ask_question",
      "args": {
        "summary": "Blocked by S3 test-integrity-guard; awaiting operator decision",
        "question": "S3 blocked promotion of staged commit."
      }
    }
  ],
  "response": "Blocked at S3."
}
EOF
	api POST "/api/tasks/$tid/approve" '{}' >/dev/null
	wait_task_state "$tid" "awaiting_reply" 120 >/dev/null
	echo "$tid"
}

T7=$(prepare_blocked_task "Bypass S3 happy path with autoMerge and secondary write" "Blocked at S3 with autoMerge=true and writes=[acme/secondary]" true '[{"repo":"acme/secondary"}]' "pkg/bypass/empty7_test.go" "TestEmpty7")
T7_STAGE="grain-qa-stage/$T7"
T7_SHA=$(curl -fsS "http://$GHSIM/_sim/branches" | jq -r --arg b "$T7_STAGE" '.[$b]')
assert_eq "4 setup T7 s3Blocked=true" "$(api GET "/api/tasks/$T7/qa" | jq -r '.s3Blocked')" "true"

T7B=$(prepare_blocked_task "Bypass S3 refusal matrix target" "Blocked at S3 with autoMerge=false and writes=[acme/secondary] for RBAC & validation matrix" false '[{"repo":"acme/secondary"}]' "pkg/bypass/empty7b_test.go" "TestEmpty7b")
T7B_STAGE="grain-qa-stage/$T7B"
T7B_SHA=$(curl -fsS "http://$GHSIM/_sim/branches" | jq -r --arg b "$T7B_STAGE" '.[$b]')
assert_eq "4 setup T7b s3Blocked=true" "$(api GET "/api/tasks/$T7B/qa" | jq -r '.s3Blocked')" "true"

# Mint real signed ES256 Hub JWT assertions.
JWT_ALICE=$(mint_jwt '{"sub":"alice@example.com","kind":"human"}')
JWT_DELEGATED=$(mint_jwt '{"sub":"alice@example.com","kind":"human","delegate":"slack-bot@example.iam.gserviceaccount.com","app":"slack-bot","grant":"dg_walk","access":"write"}')
JWT_VIEWER=$(mint_jwt '{"sub":"viewer@example.com","kind":"human"}')
JWT_CONTRIB=$(mint_jwt '{"sub":"contrib@example.com","kind":"human"}')
JWT_WIDGETS_ONLY=$(mint_jwt '{"sub":"widgets-merger@example.com","kind":"human"}')

# 4b.1 External agent token holding merge:write -> 403
c=$(http_status_and_body "$WORK_DIR/captures/refuse-agent.txt" -X POST -H "Authorization: Bearer $EXT_AGENT_TOK" -H "Content-Type: application/json" -d '{"reason":"agent bypass attempt"}' "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.1 external agent token with merge:write refused (403)" "$c" "403"

# 4b.2 Delegated human request -> 403
c=$(http_status_and_body "$WORK_DIR/captures/refuse-delegated.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_DELEGATED" -H "Content-Type: application/json" -d '{"reason":"delegated bypass attempt"}' "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.2 delegated human request refused (403)" "$c" "403"
assert_contains "4b.2 delegated refusal explains person directly required" "$(cat "$WORK_DIR/captures/refuse-delegated.txt")" "directly"

# 4b.3 Human lacking merge:write (viewer & contributor) -> 403
c=$(http_status_and_body "$WORK_DIR/captures/refuse-viewer.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_VIEWER" -H "Content-Type: application/json" -d '{"reason":"viewer bypass"}' "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.3a viewer lacking merge:write refused (403)" "$c" "403"
c=$(http_status_and_body "$WORK_DIR/captures/refuse-contrib.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_CONTRIB" -H "Content-Type: application/json" -d '{"reason":"contrib bypass"}' "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.3b contributor lacking merge:write refused (403)" "$c" "403"

# 4b.4 Human holding merge:write+tasks:write on acme/widgets ONLY, lacking write on secondary task.Writes repo acme/secondary -> 403
c=$(http_status_and_body "$WORK_DIR/captures/refuse-secondary.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_WIDGETS_ONLY" -H "Content-Type: application/json" -d '{"reason":"widgets-only bypass"}' "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.4 human lacking write on secondary task.Writes repo refused (403)" "$c" "403"
assert_contains "4b.4 refusal names acme/secondary" "$(cat "$WORK_DIR/captures/refuse-secondary.txt")" "acme/secondary"

# 4b.5 Closed task -> 400
T_CLOSED=$(api POST /api/tasks '{"title":"Closed task for bypass refusal","description":"Will be closed","repo":"acme/widgets","base":"main","approved":false}' | jq -r '.id')
api POST "/api/tasks/$T_CLOSED/close" '{"reason":"walkthrough close"}' >/dev/null
c=$(http_status_and_body "$WORK_DIR/captures/refuse-closed.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d '{"reason":"closed bypass"}' "$DAEMON/api/tasks/$T_CLOSED/qa/bypass-s3")
assert_eq "4b.5 closed task refused (400)" "$c" "400"

# 4b.6 Task with no target repo -> 400
T_NOREPO=$(api POST /api/tasks '{"title":"No-repo task for bypass refusal","description":"Standalone task","noRepo":true,"approved":false}' | jq -r '.id')
c=$(http_status_and_body "$WORK_DIR/captures/refuse-norepo.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d '{"reason":"no repo bypass"}' "$DAEMON/api/tasks/$T_NOREPO/qa/bypass-s3")
assert_eq "4b.6 task with no target repo refused (400)" "$c" "400"

# 4b.7 Empty / whitespace / 501-rune ASCII / 501-rune multi-byte UTF-8 reason -> 400
c=$(http_status_and_body "$WORK_DIR/captures/refuse-empty.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d '{"reason":""}' "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.7a empty reason refused (400)" "$c" "400"
c=$(http_status_and_body "$WORK_DIR/captures/refuse-ws.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d '{"reason":"   \t\n  "}' "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.7b whitespace reason refused (400)" "$c" "400"
REASON_501=$(python3 -c 'print("a"*501)')
c=$(http_status_and_body "$WORK_DIR/captures/refuse-501ascii.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d "{\"reason\":\"$REASON_501\"}" "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.7c 501-ASCII-rune reason refused (400)" "$c" "400"
REASON_501_UTF8=$(python3 -c 'print("é"*501)')
c=$(http_status_and_body "$WORK_DIR/captures/refuse-501utf8.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d "{\"reason\":\"$REASON_501_UTF8\"}" "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.7d 501-multibyte-rune reason refused (400)" "$c" "400"

# 4b.8 Invalid head ("abc", non-hex) -> 400
c=$(http_status_and_body "$WORK_DIR/captures/refuse-shorthead.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d '{"reason":"valid reason","head":"abc"}' "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.8a short head 'abc' refused (400)" "$c" "400"
c=$(http_status_and_body "$WORK_DIR/captures/refuse-nonhex.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d '{"reason":"valid reason","head":"zzzzzzzz"}' "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.8b non-hex head refused (400)" "$c" "400"

# 4b.9 Unknown JSON field -> 400
c=$(http_status_and_body "$WORK_DIR/captures/refuse-unknown.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d '{"reason":"ok","extra":1}' "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.9 unknown JSON field refused (400)" "$c" "400"

# 4b.10 Stale pinned head (push 2nd commit to grain-qa-stage/$T7B, submit old $T7B_SHA) -> 409 without promoting!
T7B_BRANCH=$(api GET "/api/tasks/$T7B" | jq -r '.branch')
T7B_SHA2=$(push_commit_to_bare "$WORK_DIR/widgets.git" "$T7B_STAGE" "$T7B_STAGE" "pkg/bypass/unreviewed.go" $'package bypass\nfunc Unreviewed() {}\n' $'feat: newer unreviewed commit\n\nBody.')
c=$(http_status_and_body "$WORK_DIR/captures/refuse-stalehead.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d "{\"reason\":\"reviewed commit 1 only\",\"head\":\"$T7B_SHA\"}" "$DAEMON/api/tasks/$T7B/qa/bypass-s3")
assert_eq "4b.10 stale pinned head refused (409)" "$c" "409"
if curl -fsS "http://$GHSIM/_sim/branches" | jq -e --arg b "$T7B_BRANCH" 'has($b)' >/dev/null; then
	fail "4b.10 stale head promoted $T7B_BRANCH despite 409!"
else
	ok "4b.10 stale head did NOT promote newer unreviewed commit $T7B_SHA2"
fi

# 4b.11 Unblocked task -> 409
T_UNBLOCKED=$(api POST /api/tasks '{"title":"Unblocked task","description":"No S3 failure","repo":"acme/widgets","base":"main","approved":false}' | jq -r '.id')
c=$(http_status_and_body "$WORK_DIR/captures/refuse-unblocked.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d '{"reason":"not blocked"}' "$DAEMON/api/tasks/$T_UNBLOCKED/qa/bypass-s3")
assert_eq "4b.11 unblocked task refused (409)" "$c" "409"

# 4b.12 Already has open PR with NO staged commits on grain-qa-stage/<id> -> 409 (even if an S3 fail run is recorded!)
# T1 already has an open PR and no grain-qa-stage/$T1 branch. Record a new S3 fail run on T1 and try bypassing.
T1_PR_HEAD=$(curl -fsS "http://$GHSIM/_sim/branches" | jq -r --arg b "$(api GET "/api/tasks/$T1" | jq -r '.branch')" '.[$b]')
api POST "/api/tasks/$T1/qa/check-runs" "{\"entries\":[{\"run\":{\"runId\":\"post-pr-1\",\"stage\":\"S3\",\"checkId\":\"golden:test-integrity-guard\",\"arm\":\"treatment\",\"commitSha\":\"$T1_PR_HEAD\",\"verdict\":\"fail\"}}]}" >/dev/null
c=$(http_status_and_body "$WORK_DIR/captures/refuse-openpr.txt" -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d '{"reason":"try bypass on open PR"}' "$DAEMON/api/tasks/$T1/qa/bypass-s3")
assert_eq "4b.12 task with open PR and no staged commits refused (409)" "$c" "409"

# 4a: Happy-path bypass (API on postgres, or API when SKIP_HAPPY_BYPASS!=1; on sqlite Playwright can drive T7 directly!)
if [[ "${SKIP_HAPPY_BYPASS:-0}" != "1" ]]; then
	REASON_500=$(python3 -c 'print("é"*500)')
	T7_BYPASS=$(curl -fsS -X POST -H "X-Grain-Hub-Assertion: $JWT_ALICE" -H "Content-Type: application/json" -d "{\"reason\":\"$REASON_500\",\"head\":\"${T7_SHA:0:12}\"}" "$DAEMON/api/tasks/$T7/qa/bypass-s3")
	echo "$T7_BYPASS" >"$WORK_DIR/captures/t7-bypass.json"
	T7_DETAIL_AFTER=$(api GET "/api/tasks/$T7")
	T7_QA_AFTER=$(api GET "/api/tasks/$T7/qa")
	echo "$T7_DETAIL_AFTER" >"$WORK_DIR/captures/t7-detail-after.json"
	echo "$T7_QA_AFTER" >"$WORK_DIR/captures/t7-qa-after.json"
	T7_BRANCH=$(echo "$T7_DETAIL_AFTER" | jq -r '.branch')
	assert_eq "4a happy bypass opened PR" "$(echo "$T7_BYPASS" | jq -r '.number > 0')" "true"
	assert_eq "4a autoMerge withdrawn" "$(echo "$T7_DETAIL_AFTER" | jq -r '.autoMerge')" "false"
	assert_eq "4a auto_merge_off transition recorded" "$(echo "$T7_DETAIL_AFTER" | jq -r '[.transitions[] | select(.state=="auto_merge_off" and (.reason // "" | contains("bypassed")))] | length >= 1')" "true"
	assert_eq "4a S5 triageDecision requires human review" "$(echo "$T7_DETAIL_AFTER" | jq -r '.qaSummary.triageDecision')" "require_human"
	assert_eq "4a approval merge gate needed" "$(echo "$T7_DETAIL_AFTER" | jq -r '[.mergeGates[] | select(.id=="approval" and .state=="needed")] | length >= 1')" "true"
	assert_eq "4a s3Blocked cleared after bypass" "$(echo "$T7_QA_AFTER" | jq -r '.s3Blocked // false')" "false"
	assert_eq "4a S5 qa:merge-triage check run persisted" "$(echo "$T7_QA_AFTER" | jq -r '[.checkRuns[] | select(.stage=="S5" and .checkId=="qa:merge-triage")] | length >= 1')" "true"
	if [[ "$STORE" == "postgres" ]]; then
		AWAITS_LINK=$(psql "$PG_DSN" -At -c "SELECT count(*) FROM task_link WHERE task_id = '$T7' AND kind = 'auto-merge-awaits-person';")
		assert_eq "4a PostgreSQL persisted auto-merge-awaits-person link" "$((AWAITS_LINK >= 1))" "1"
	else
		AWAITS_LINK=$(sqlite3 "$WORK_DIR/data/store/grain.db" "SELECT count(*) FROM task_link WHERE task_id = '$T7' AND kind = 'auto-merge-awaits-person';")
		assert_eq "4a SQLite persisted auto-merge-awaits-person link" "$((AWAITS_LINK >= 1))" "1"
	fi
	T7_AUDIT_CNT=$(echo "$T7_QA_AFTER" | jq -r --arg sha "$T7_SHA" '[.checkRuns[] | select(.checkId=="qa:s3-operator-bypass" and .verdict=="bypassed" and .commitSha==$sha)] | length')
	assert_eq "4a qa:s3-operator-bypass audit check run recorded" "$T7_AUDIT_CNT" "1"
	T7_ACTOR=$(echo "$T7_QA_AFTER" | jq -r '[.checkRuns[] | select(.checkId=="qa:s3-operator-bypass")][0].detailJson | fromjson | .bypassedBy')
	assert_eq "4a audit actor is alice@example.com" "$T7_ACTOR" "alice@example.com"
	T7_COMMENT=$(echo "$T7_DETAIL_AFTER" | jq -r '.comments[-1].body // ""')
	assert_contains "4a audit comment names alice@example.com" "$T7_COMMENT" "alice@example.com"
	if curl -fsS "http://$GHSIM/_sim/branches" | jq -e --arg pr "$T7_BRANCH" --arg stg "$T7_STAGE" 'has($pr) and (has($stg) | not)' >/dev/null; then
		ok "4a $T7_STAGE promoted to $T7_BRANCH and deleted on remote"
	else
		fail "4a unexpected remote branches after bypass: $(curl -fsS "http://$GHSIM/_sim/branches")"
	fi
fi

# Also prepare T8 for UI Playwright stale-head 409 check if KEEP_RUNNING=1.
if [[ "$KEEP_RUNNING" == "1" ]]; then
	T8=$(prepare_blocked_task "UI stale-head 409 test task" "Used by Playwright to test stale-head 409 inside <g-bypass-s3-dialog>" false '[]' "pkg/bypass/ui_test.go" "TestUI")
	T8_STAGE="grain-qa-stage/$T8"
	T8_SHA=$(curl -fsS "http://$GHSIM/_sim/branches" | jq -r --arg b "$T8_STAGE" '.[$b]')
	cat >"$WORK_DIR/ui-state.json" <<EOF
{
  "daemon": "$DAEMON",
  "ghsim": "http://$GHSIM",
  "code": "$CODE",
  "bareRepo": "$WORK_DIR/widgets.git",
  "t2DeletedBaseId": "$T2",
  "t3WarnId": "$T3",
  "t4BlockedId": "$T4",
  "t6ActionsId": "$T6",
  "t7HappyId": "$T7",
  "t7Sha": "$T7_SHA",
  "t8StaleId": "$T8",
  "t8StageBranch": "$T8_STAGE",
  "t8Sha": "$T8_SHA"
}
EOF
fi

printf "\n=== Summary (STORE=%s): %d passed, %d failed ===\n" "$STORE" "$PASS_COUNT" "$FAIL_COUNT"
if [[ "$FAIL_COUNT" -gt 0 ]]; then
	printf "Failures:\n" >&2
	for f in "${FINDINGS[@]}"; do
		printf "  - %s\n" "$f" >&2
	done
	exit 1
fi
