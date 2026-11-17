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

# Full live walkthrough for docs/test-plans/2026-11-16.md §3
# ("Review findings and merge gates") against a real `grain daemon`
# over STORE=sqlite or STORE=postgres.
#
# Set EXPECT_FIXED=0 (default) to record bug reproductions without failing the
# run early; set EXPECT_FIXED=1 to require every check to pass strictly.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
source "$HERE/env.sh"

EXPECT_FIXED=${EXPECT_FIXED:-0}
PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}
NODE_PATH=${NODE_PATH:-/home/debian/work/ui-ng/node_modules}
export PLAYWRIGHT_BROWSERS_PATH NODE_PATH

PASSES=0
FAILS=0
REPROS=0

pass() { PASSES=$((PASSES + 1)); echo "[PASS] $*"; }
fail() { FAILS=$((FAILS + 1)); echo "[FAIL] $*"; }
repro() { REPROS=$((REPROS + 1)); echo "[BUG-REPRO] $*"; }

check() {
	local cond=$1 msg=$2
	if [[ "$cond" == "1" ]]; then
		pass "$msg"
	else
		fail "$msg"
	fi
}

check_bug() {
	local fixed_cond=$1 repro_msg=$2 fixed_msg=$3
	if [[ "$fixed_cond" == "1" ]]; then
		pass "$fixed_msg"
	elif [[ "$EXPECT_FIXED" == "1" ]]; then
		fail "$fixed_msg (still reproducing: $repro_msg)"
	else
		repro "$repro_msg"
	fi
}

fresh_jwt() {
	curl -fsS "http://$GHSIM/_sim/jwt?email=$1"
}

walk_ui() {
	NODE_PATH="$NODE_PATH" PLAYWRIGHT_BROWSERS_PATH="$PLAYWRIGHT_BROWSERS_PATH" node "$HERE/walk_ui.mjs" "$@"
}

wait_task_states() {
	local id=$1 want_re=$2 max_wait=${3:-120}
	for _ in $(seq "$max_wait"); do
		local st
		st=$(api GET "/api/tasks/$id" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("state",""))')
		if [[ "$st" =~ $want_re ]]; then
			return 0
		fi
		sleep 0.25
	done
	echo "timed out waiting for task $id to match /$want_re/ (last=$st)" >&2
	api GET "/api/tasks/$id" >&2
	return 1
}

wait_pr_on_task() {
	local id=$1 max_wait=${2:-120}
	for _ in $(seq "$max_wait"); do
		local pr
		pr=$(api GET "/api/tasks/$id" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("pullRequest","") or "")')
		if [[ -n "$pr" ]]; then
			echo "$pr"
			return 0
		fi
		sleep 0.25
	done
	echo "timed out waiting for PR on task $id" >&2
	return 1
}

wait_reviews_for_parent() {
	local parent_id=$1 min_count=$2 max_wait=${3:-160}
	for _ in $(seq "$max_wait"); do
		local cnt
		cnt=$(api GET "/api/tasks?limit=200" | python3 -c '
import json,sys
pid = sys.argv[1]
tasks = json.load(sys.stdin)
revs = [t for t in tasks if t.get("generatedFrom") == pid]
done = [t for t in revs if t.get("state") in ("closed", "completed", "awaiting_submit")]
print(f"{len(revs)} {len(done)}")
' "$parent_id")
		local total finished
		read -r total finished <<<"$cnt"
		if [[ "$total" -ge "$min_count" && "$finished" -ge "$min_count" ]]; then
			return 0
		fi
		sleep 0.25
	done
	echo "timed out waiting for $min_count finished reviews of task $parent_id (got $cnt)" >&2
	return 1
}

trap 'stop_env "$WORK_DIR"' EXIT

echo "================================================================"
echo "  Walkthrough §3 (STORE=$STORE, EXPECT_FIXED=$EXPECT_FIXED)"
echo "================================================================"

init_env
start_ghsim
start_daemon
setup_principals_and_repo
wait_declarations_synced

# Set default prompt-regex rules for fake-agy.py.
# Order matters: more specific review prompts first!
write_rules() {
	cat >"$WORK_DIR/rules.json"
}

# ==============================================================================
# ITEM 1 & ITEM 3 (part A): Security review + Code review + Targeted review
# ==============================================================================
echo ""
echo "=== ITEM 1 & ITEM 3A: All 3 review gates + Accept findings + Reconcile + Re-review ==="

# Order the reviews so Code review (every-pr) finishes FIRST (setting
# LinkAutoMergeAwaitsPerson = "review <code_id> left findings"), and Security
# review finishes AFTER (so we directly test Bug 4: accepting security findings
# while LinkAutoMergeAwaitsPerson names the code review!).
write_rules <<'JSON'
[
  {
    "match": "WALK-CODE-REVIEW",
    "action": {
      "kind": "mcp_sequence",
      "calls": [
        {"tool": "add_review_comment", "args": {"path": "README.md", "line": 1, "body": "Code review finding: missing usage example"}},
        {"tool": "comment_on_issue", "args": {"comment": "Code review left 1 finding."}}
      ]
    }
  },
  {
    "match": "Targeted review \\(",
    "action": {
      "kind": "mcp_sequence",
      "calls": [
        {"tool": "add_review_comment", "args": {"path": "payments/charge.go", "line": 1, "body": "Targeted finding: missing idempotency key"}},
        {"tool": "comment_on_issue", "args": {"comment": "Targeted review left 1 finding."}}
      ]
    }
  },
  {
    "match": "Security review:",
    "action": {
      "kind": "mcp_sequence",
      "calls": [
        {"tool": "run_command", "args": {"command": "sleep 1.2", "description": "pausing so code review finishes first"}},
        {"tool": "add_review_comment", "args": {"path": "pkg/ci/ci.go", "line": 1, "body": "Security finding: unvalidated CI input"}},
        {"tool": "comment_on_issue", "args": {"comment": "Security review left 1 finding."}}
      ]
    }
  }
]
JSON

T1=$(api POST /api/tasks '{"title":"Add payment CI hook","description":"Touch pkg/ci/ci.go, payments/charge.go, README.md and push.","repo":"acme/widgets","approved":true,"autoMerge":true}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
cat >"$WORK_DIR/action-${T1}.json" <<'JSON'
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "cd work && echo '// ci change' >> pkg/ci/ci.go && echo '// charge change' >> payments/charge.go && echo 'updated' >> README.md && git add pkg/ci/ci.go payments/charge.go README.md && git commit -m 'feat: add payment CI hook' && git push origin HEAD",
        "description": "committing and pushing payment CI hook"
      }
    },
    {"tool": "comment_on_issue", "args": {"comment": "Pushed payment CI hook."}}
  ]
}
JSON

T1_PR=$(wait_pr_on_task "$T1" 160)
echo "Task $T1 opened PR $T1_PR"
wait_reviews_for_parent "$T1" 3 240
# Give orchestrator a couple of ticks to record all findings & holds.
sleep 2.0

T1_JSON=$(api GET "/api/tasks/$T1")
REVIEWS_JSON=$(api GET "/api/tasks?limit=200" | python3 -c '
import json,sys
pid = sys.argv[1]
tasks = json.load(sys.stdin)
revs = [t for t in tasks if t.get("generatedFrom") == pid]
print(json.dumps([{"id":r["id"],"title":r["title"],"state":r["state"]} for r in revs]))
' "$T1")
echo "Reviews filed for task $T1: $REVIEWS_JSON"

# Check Bug 5: non-security review finishing while LinkSecurityHold is active
# must NOT hijack gate="security-review" or source="security-findings"!
BUG5_OK=$(python3 -c '
import json,sys
d = json.loads(sys.stdin.read())
sec_id = d.get("securityReviewTask","")
bad_rf = [r for r in (d.get("reviewFindings") or []) if r.get("gate") == "security-review" and r.get("reviewTask") != sec_id]
bad_hr = [h for h in (d.get("humanReviewHold") or []) if h.get("source") == "security-findings" and h.get("reviewTask") != sec_id]
print(int(len(bad_rf) == 0 and len(bad_hr) == 0))
' <<<"$T1_JSON")
check_bug "$BUG5_OK" \
  "BUG-5: non-security review dropped LinkSecurityHold and recorded gate=security-review / source=security-findings under its own reviewTask" \
  "Item 1 (Bug 5 fixed): only the security review task records gate=security-review / source=security-findings"

# Check Item 3 titles: Security review, Code review (<pr>), Targeted review (payments)
TITLE_CHECKS=$(python3 -c '
import json,sys
revs = json.loads(sys.argv[1])
pr = sys.argv[2]
titles = [r["title"] for r in revs]
sec = any(t == "Security review: Add payment CI hook" for t in titles)
code = any(t == f"Code review: Add payment CI hook ({pr})" for t in titles)
targ = any(t == "Targeted review (payments): Add payment CI hook" for t in titles)
print(f"{int(sec)} {int(code)} {int(targ)}")
' "$REVIEWS_JSON" "$T1_PR")
read -r HAS_SEC_TITLE HAS_CODE_TITLE HAS_TARG_TITLE <<<"$TITLE_CHECKS"
check "$HAS_SEC_TITLE" "Item 3: Security review titled 'Security review: Add payment CI hook'"
check "$HAS_CODE_TITLE" "Item 3: Code review titled 'Code review: Add payment CI hook ($T1_PR)'"
check "$HAS_TARG_TITLE" "Item 3: Targeted review titled 'Targeted review (payments): Add payment CI hook'"

# Inspect T1 mergeGates on backend
GATES_SUMMARY=$(python3 -c '
import json,sys
d = json.loads(sys.stdin.read())
by_id = {g["id"]: g for g in (d.get("mergeGates") or [])}
print(json.dumps({k: {"state": v.get("state"), "acceptable": bool(v.get("acceptable")), "reviewTask": v.get("reviewTask")} for k, v in by_id.items()}))
' <<<"$T1_JSON")
echo "Task $T1 gates: $GATES_SUMMARY"

read -r SEC_ACC REV_ACC TARG_ACC TARG_PRESENT <<<"$(python3 -c '
import json,sys
g = json.loads(sys.stdin.read())
print(
  int(g.get("security-review",{}).get("acceptable",False)),
  int(g.get("review",{}).get("acceptable",False)),
  int(g.get("targeted-review",{}).get("acceptable",False)),
  int("targeted-review" in g and g["targeted-review"].get("state") == "findings")
)
' <<<"$GATES_SUMMARY")"
check "$SEC_ACC" "Item 1: security-review gate has state=findings & acceptable=true"
check "$REV_ACC" "Item 1: review gate has state=findings & acceptable=true"
check "$TARG_PRESENT" "Item 3: targeted-review gate present with state=findings"
check_bug "$TARG_ACC" \
  "BUG-1: targeted-review gate has acceptable=false even with a real targeted review task behind it" \
  "Item 1/3 (Bug 1 fixed): targeted-review gate has acceptable=true"

# Check Playwright on /next/task/<T1>?tab=changes as ALICE (full) and BOB (repo-scoped writer)
ALICE_UI=$(walk_ui inspect-next --url "$DAEMON" --jwt "$(fresh_jwt alice@example.com)" --task "$T1" --openForceMerge)
echo "Alice /next/ UI on T1: $ALICE_UI"
BOB_UI=$(walk_ui inspect-next --url "$DAEMON" --jwt "$(fresh_jwt bob@example.com)" --task "$T1" --openForceMerge)
echo "Bob /next/ UI on T1: $BOB_UI"

read -r ALICE_SEES_SEC_ACC ALICE_SEES_TARG_ACC ALICE_SEES_OVERRIDE ALICE_SEES_RELEASE ALICE_FM_HINT <<<"$(python3 -c '
import json,sys
d = json.loads(sys.stdin.read())
btns = {b["testid"] for b in d.get("acceptButtons",[])}
h = d.get("hold",{})
fm = d.get("forceMerge",{})
print(
  int("accept-findings-security-review" in btns),
  int("accept-findings-targeted-review" in btns),
  int(bool(h.get("overrideVisible"))),
  int(bool(h.get("releaseVisible"))),
  int(bool(fm.get("acceptHintVisible")))
)
' <<<"$ALICE_UI")"

check "$ALICE_SEES_SEC_ACC" "Item 1 (/next/ Alice): Accept findings… visible on security-review gate"
check_bug "$ALICE_SEES_TARG_ACC" \
  "BUG-1 (/next/ Alice): Accept findings… missing on targeted-review gate" \
  "Item 1/3 (/next/ Alice, Bug 1 fixed): Accept findings… visible on targeted-review gate"
check "$ALICE_SEES_OVERRIDE" "Item 2 (/next/ Alice): Override review gate… offered on findings-only hold"
check "$((1 - ALICE_SEES_RELEASE))" "Item 2 (/next/ Alice): Release hold hidden on findings-only hold"
check "$ALICE_FM_HINT" "Item 1 (/next/ Alice): Force merge dialog shows Accept findings hint"

read -r BOB_SEES_ACC BOB_SEES_OVERRIDE BOB_SEES_RELEASE <<<"$(python3 -c '
import json,sys
d = json.loads(sys.stdin.read())
btns = d.get("acceptButtons",[])
h = d.get("hold",{})
print(int(len(btns) > 0), int(bool(h.get("overrideVisible"))), int(bool(h.get("releaseVisible"))))
' <<<"$BOB_UI")"
check "$((1 - BOB_SEES_OVERRIDE))" "Item 2 (/next/ Bob): Override review gate… hidden from repo-scoped writer"
check "$((1 - BOB_SEES_RELEASE))" "Item 2 (/next/ Bob): Release hold hidden from repo-scoped writer"
check_bug "$((1 - BOB_SEES_ACC))" \
  "BUG-2 (/next/ Bob): repo-scoped writer sees Accept findings… button (clicking it returns 403)" \
  "Item 1 (/next/ Bob, Bug 2 fixed): Accept findings… hidden from repo-scoped writer"

if [[ -n "${CAPTURE_SCREENSHOTS_DIR:-}" ]]; then
	echo "Capturing screenshots of /next/task/$T1?tab=changes (Alice & Bob) into $CAPTURE_SCREENSHOTS_DIR"
	walk_ui capture --url "$DAEMON" --jwt "$(fresh_jwt alice@example.com)" \
	  --path "/next/task/$T1?tab=changes" --waitFor '[data-testid="merge-gate-security-review"]' \
	  --out "$CAPTURE_SCREENSHOTS_DIR" --prefix "01-next-changes-alice"
	walk_ui capture --url "$DAEMON" --jwt "$(fresh_jwt bob@example.com)" \
	  --path "/next/task/$T1?tab=changes" --waitFor '[data-testid="merge-gate-security-review"]' \
	  --out "$CAPTURE_SCREENSHOTS_DIR" --prefix "02-next-changes-bob"
fi

# Verify Bob gets 403 if calling accept-findings directly
BOB_ACC_RESP=$(api_as "$(fresh_jwt bob@example.com)" POST "/api/tasks/$T1/accept-findings" '{"gate":"security-review","reason":"bob attempt"}')
BOB_ACC_CODE=$(tail -n 1 <<<"$BOB_ACC_RESP")
check "$([[ "$BOB_ACC_CODE" == "403" ]] && echo 1 || echo 0)" "Item 1 (API Bob): POST /accept-findings returns 403 for repo-scoped writer (got $BOB_ACC_CODE)"

# Item 2: Release hold on findings-only hold returns 409 review_findings_hold (not 500)
REL_FINDINGS_RESP=$(api_as "$(fresh_jwt alice@example.com)" POST "/api/tasks/$T1/human-review/release" '')
REL_FINDINGS_CODE=$(tail -n 1 <<<"$REL_FINDINGS_RESP")
REL_FINDINGS_BODY=$(sed '$d' <<<"$REL_FINDINGS_RESP")
echo "Release hold on findings-only T1: HTTP $REL_FINDINGS_CODE $REL_FINDINGS_BODY"
REL_FINDINGS_OK=$(python3 -c '
import json,sys
code = sys.argv[1]
b = json.loads(sys.argv[2])
print(int(code == "409" and b.get("code") == "review_findings_hold" and "cannot be cleared by Submit or Release hold" in b.get("error","")))
' "$REL_FINDINGS_CODE" "$REL_FINDINGS_BODY")
check "$REL_FINDINGS_OK" "Item 2 (API): Release hold on findings-only hold answers 409 review_findings_hold with verbatim message"

# Classic /tasks/<T1> check on findings-only hold
CLASSIC_T1=$(walk_ui inspect-classic --url "$DAEMON" --jwt "$(fresh_jwt alice@example.com)" --task "$T1")
echo "Classic /tasks/$T1 (findings-only): $CLASSIC_T1"
read -r CLASSIC_NO_REL CLASSIC_HAS_TARG <<<"$(python3 -c '
import json,sys
d = json.loads(sys.stdin.read())
print(int(not d.get("hold",{}).get("releaseVisible",False)), int(bool(d.get("hasTargetedReviewText"))))
' <<<"$CLASSIC_T1")"
check "$CLASSIC_NO_REL" "Item 2 (Classic Alice): Release hold hidden on findings-only hold"
check "$CLASSIC_HAS_TARG" "Item 3 (Classic): Targeted review gate renders on classic /tasks/<id>"

# Item 1: Alice accepts security-review findings through Playwright on /next/task/<T1>?tab=changes
ACCEPT_SEC_UI=$(walk_ui inspect-next --url "$DAEMON" --jwt "$(fresh_jwt alice@example.com)" --task "$T1" --clickAccept security-review --reason "Validated safe in walkthrough")
echo "Alice Accept findings (security-review) UI result: $ACCEPT_SEC_UI"
read -r ACC_DLG_OK ACC_POST_200 <<<"$(python3 -c '
import json,sys
r = (json.loads(sys.stdin.read()).get("acceptClickResult") or {})
print(
  int(bool(r.get("initiallyDisabled")) and bool(r.get("enabledAfterFill"))),
  int(r.get("status") == 200),
)
' <<<"$ACCEPT_SEC_UI")"
check "$ACC_DLG_OK" "Item 1 (/next/ Alice): Accept findings dialog disables submit until reason entered"
check_bug "$ACC_POST_200" \
  "BUG-6 (/next/ Alice): Accept findings dialog sent branch name from DiffPreview.head as head SHA and failed HTTP 400 ('has moved since you looked')" \
  "Item 1 (/next/ Alice, Bug 6 fixed): Accept findings dialog POSTed 200 with valid head SHA"

# If Bug 6 blocked the UI click (on unmodified HEAD), accept via API directly so we still test reconcile ticks (Bug 4/5) and re-review (Item 1B)!
if [[ "$ACC_POST_200" != "1" ]]; then
	SEC_REV_ID=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("securityReviewTask",""))' <<<"$T1_JSON")
	API_ACC_CODE=$(api_as "$(fresh_jwt alice@example.com)" POST "/api/tasks/$T1/accept-findings" "{\"gate\":\"security-review\",\"reviewTask\":\"$SEC_REV_ID\",\"reason\":\"Validated safe in walkthrough (API fallback after Bug 6)\"}" | tail -n 1)
	echo "API accept-findings fallback for reviewTask=$SEC_REV_ID: HTTP $API_ACC_CODE"
fi

# Wait >= 5 reconcile ticks (poll interval = 500ms -> 3.5s = ~7 ticks) and verify security-review stays passed & hold stays gone!
sleep 3.5
T1_AFTER_TICKS=$(api GET "/api/tasks/$T1")
read -r SEC_STILL_PASSED HOLD_STILL_GONE CODE_STILL_FINDINGS TARG_STILL_FINDINGS <<<"$(python3 -c '
import json,sys
d = json.loads(sys.stdin.read())
g = {x["id"]: x["state"] for x in (d.get("mergeGates") or [])}
hr = d.get("humanReviewHold") or []
print(
  int(g.get("security-review") == "passed"),
  int(len(hr) == 0),
  int(g.get("review") == "findings"),
  int(g.get("targeted-review") == "findings"),
)
' <<<"$T1_AFTER_TICKS")"
check_bug "$((SEC_STILL_PASSED & HOLD_STILL_GONE))" \
  "BUG-4/5: security-review did not stay passed/unheld across reconcile ticks (sec_passed=$SEC_STILL_PASSED hold_gone=$HOLD_STILL_GONE)" \
  "Item 1 (Bugs 4 & 5 fixed): accepted security findings stay accepted across >=5 reconcile ticks even when other reviews finished first"
check "$CODE_STILL_FINDINGS" "Item 1: code review findings still stand after accepting security findings"
check "$TARG_STILL_FINDINGS" "Item 1: targeted review findings still stand after accepting security findings"

# Test accepting targeted-review findings via API (reproduces Bug 1 400 on unmodified HEAD)
ACC_TARG_RESP=$(api_as "$(fresh_jwt alice@example.com)" POST "/api/tasks/$T1/accept-findings" '{"gate":"targeted-review","reason":"Idempotency handled upstream"}')
ACC_TARG_CODE=$(tail -n 1 <<<"$ACC_TARG_RESP")
ACC_TARG_BODY=$(sed '$d' <<<"$ACC_TARG_RESP")
echo "POST /accept-findings gate=targeted-review: HTTP $ACC_TARG_CODE $ACC_TARG_BODY"
check_bug "$([[ "$ACC_TARG_CODE" == "200" ]] && echo 1 || echo 0)" \
  "BUG-1 (API): POST /accept-findings with gate=targeted-review returned HTTP $ACC_TARG_CODE ($ACC_TARG_BODY)" \
  "Item 1/3 (API, Bug 1 fixed): POST /accept-findings with gate=targeted-review succeeded (HTTP 200)"

# Item 1: Re-review raises new findings -> gate returns to findings and hold returns!
echo ""
echo "=== ITEM 1B: Re-review raising new findings on T1 ==="
write_rules <<'JSON'
[
  {
    "match": "WALK-CODE-REVIEW|Targeted review \\(",
    "action": {
      "kind": "comment_on_issue",
      "comment": "Clean on head 2."
    }
  },
  {
    "match": "Security review:",
    "action": {
      "kind": "mcp_sequence",
      "calls": [
        {"tool": "add_review_comment", "args": {"path": "pkg/ci/ci.go", "line": 2, "body": "New security finding on head 2: shell interpolation"}},
        {"tool": "comment_on_issue", "args": {"comment": "Re-review left new security finding."}}
      ]
    }
  }
]
JSON

# Trigger a real follow-up run on T1 that pushes commit 2 to its branch, causing
# finishWithPullRequest -> holdForTrustBoundary to file a fresh security re-review!
cat >"$WORK_DIR/action-${T1}.json" <<'JSON'
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "cd work && echo '// second head change' >> pkg/ci/ci.go && git add pkg/ci/ci.go && git commit -m 'fix: update ci.go for head 2' && git push origin HEAD",
        "description": "pushing second commit on T1 for re-review"
      }
    },
    {"tool": "comment_on_issue", "args": {"comment": "Pushed head 2."}}
  ]
}
JSON
api POST "/api/tasks/$T1/run-now" '{}' >/dev/null

# Wait for the follow-up run and the new security review to complete.
REREV_OK=0
for _ in $(seq 160); do
	T1_NOW=$(api GET "/api/tasks/$T1")
	REREV_OK=$(python3 -c '
import json,sys
d = json.loads(sys.stdin.read())
g = {x["id"]: x for x in (d.get("mergeGates") or [])}
hr = d.get("humanReviewHold") or []
sec = g.get("security-review", {})
has_new = any("shell interpolation" in (h.get("reason","")) for h in hr) or "shell interpolation" in (sec.get("detail",""))
print(int(sec.get("state") == "findings" and has_new and len(hr) > 0))
' <<<"$T1_NOW")
	if [[ "$REREV_OK" == "1" ]]; then
		break
	fi
	sleep 0.25
done
check "$REREV_OK" "Item 1B: re-review on pushed head 2 raised new security finding and restored security-findings hold"

# ==============================================================================
# ITEM 2: Mixed hold (security-findings + requested) on /next/ and classic
# ==============================================================================
echo ""
echo "=== ITEM 2: Mixed hold (security-findings + requested) on /next/ & classic ==="
write_rules <<'JSON'
[
  {
    "match": "Security review|trust-boundary|trust boundary",
    "action": {
      "kind": "mcp_sequence",
      "calls": [
        {"tool": "add_review_comment", "args": {"path": "pkg/ci/ci.go", "line": 1, "body": "Mixed-hold security finding: unpinned image"}},
        {"tool": "request_human_review", "args": {"category": "product-decision", "reason": "Confirm rollout window with operator"}},
        {"tool": "comment_on_issue", "args": {"comment": "Left both a security finding and a human review request."}}
      ]
    }
  },
  {
    "match": "WALK-CODE-REVIEW",
    "action": {"kind": "nothing_needed", "reason": "code review clean"}
  }
]
JSON

T2=$(api POST /api/tasks '{"title":"Update CI base image","description":"Touch pkg/ci/ci.go","repo":"acme/widgets","approved":true,"autoMerge":true}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
cat >"$WORK_DIR/action-${T2}.json" <<'JSON'
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "cd work && echo '// image bump' >> pkg/ci/ci.go && git add pkg/ci/ci.go && git commit -m 'chore: bump ci base image' && git push origin HEAD",
        "description": "pushing CI image bump"
      }
    },
    {"tool": "comment_on_issue", "args": {"comment": "Pushed image bump."}}
  ]
}
JSON
T2_PR=$(wait_pr_on_task "$T2" 160)
wait_reviews_for_parent "$T2" 2 200
sleep 2.0

T2_JSON=$(api GET "/api/tasks/$T2")
T2_HOLD_CNT=$(python3 -c 'import json,sys; rs=json.load(sys.stdin).get("humanReviewHold") or []; print(len(rs), [r.get("source") for r in rs])' <<<"$T2_JSON")
echo "T2 mixed hold reasons: $T2_HOLD_CNT"
check "$([[ "$T2_HOLD_CNT" == 2* ]] && echo 1 || echo 0)" "Item 2: mixed hold has both security-findings and requested reasons"

# Check /next/ mixed hold under Alice and Bob
ALICE_T2=$(walk_ui inspect-next --url "$DAEMON" --jwt "$(fresh_jwt alice@example.com)" --task "$T2")
BOB_T2=$(walk_ui inspect-next --url "$DAEMON" --jwt "$(fresh_jwt bob@example.com)" --task "$T2")
echo "Alice /next/ mixed hold: $ALICE_T2"
echo "Bob /next/ mixed hold: $BOB_T2"
read -r A2_REL A2_OVR A2_NOTE <<<"$(python3 -c '
import json,sys
h = json.loads(sys.stdin.read()).get("hold",{})
print(int(bool(h.get("releaseVisible"))), int(bool(h.get("overrideVisible"))), int("other reasons only" in h.get("noteText","")))
' <<<"$ALICE_T2")"
read -r B2_REL B2_OVR <<<"$(python3 -c '
import json,sys
h = json.loads(sys.stdin.read()).get("hold",{})
print(int(bool(h.get("releaseVisible"))), int(bool(h.get("overrideVisible"))))
' <<<"$BOB_T2")"
check "$A2_REL" "Item 2 (/next/ Alice): Release hold visible on mixed hold"
check "$A2_OVR" "Item 2 (/next/ Alice): Override review gate… visible on mixed hold"
check "$A2_NOTE" "Item 2 (/next/ Alice): card notes 'Release hold releases the other reasons only.'"
check "$((1 - B2_REL))" "Item 2 (/next/ Bob): Release hold hidden from repo-scoped writer"
check "$((1 - B2_OVR))" "Item 2 (/next/ Bob): Override review gate… hidden from repo-scoped writer"

# Check classic /tasks/<T2> mixed hold under Bob (reproduces Bug 3!)
CLASSIC_BOB_T2=$(walk_ui inspect-classic --url "$DAEMON" --jwt "$(fresh_jwt bob@example.com)" --task "$T2")
echo "Classic /tasks/$T2 as Bob: $CLASSIC_BOB_T2"
CLASSIC_BOB_REL_ENABLED=$(python3 -c '
import json,sys
h = json.loads(sys.stdin.read()).get("hold",{})
print(int(bool(h.get("releaseVisible")) and not bool(h.get("releaseDisabled"))))
' <<<"$CLASSIC_BOB_T2")
check_bug "$((1 - CLASSIC_BOB_REL_ENABLED))" \
  "BUG-3 (Classic Bob): Release hold offered enabled to repo-scoped writer (clicking it returns 403)" \
  "Item 2 (Classic Bob, Bug 3 fixed): Release hold not offered enabled to repo-scoped writer"

if [[ -n "${CAPTURE_SCREENSHOTS_DIR:-}" ]]; then
	echo "Capturing screenshots of classic /tasks/$T2 (Bob) into $CAPTURE_SCREENSHOTS_DIR"
	walk_ui capture --url "$DAEMON" --jwt "$(fresh_jwt bob@example.com)" \
	  --path "/tasks/$T2" --waitFor '[data-testid="human-review-hold"]' \
	  --out "$CAPTURE_SCREENSHOTS_DIR" --prefix "03-classic-mixed-bob"
fi

# Click Release hold on Classic as Alice -> releases ONLY requested reason, keeps security-findings!
CLASSIC_ALICE_REL=$(walk_ui inspect-classic --url "$DAEMON" --jwt "$(fresh_jwt alice@example.com)" --task "$T2" --clickRelease)
echo "Classic /tasks/$T2 Release hold as Alice: $CLASSIC_ALICE_REL"
REL_MIXED_OK=$(python3 -c '
import json,sys
r = (json.loads(sys.stdin.read()).get("releaseClickResult") or {})
if r.get("status") != 200:
    print(0)
else:
    body = json.loads(r.get("body","{}"))
    rs = body.get("reasons") or []
    print(int(len(rs) == 1 and rs[0].get("source") == "security-findings"))
' <<<"$CLASSIC_ALICE_REL")
check "$REL_MIXED_OK" "Item 2 (Classic Alice): mixed Release hold returns 200 with remaining security-findings reason (not empty)"

# Verify T2 now has only security-findings remaining and /next/ hides Release hold
ALICE_T2_POST=$(walk_ui inspect-next --url "$DAEMON" --jwt "$(fresh_jwt alice@example.com)" --task "$T2")
read -r A2_POST_REL A2_POST_OVR <<<"$(python3 -c '
import json,sys
h = json.loads(sys.stdin.read()).get("hold",{})
print(int(bool(h.get("releaseVisible"))), int(bool(h.get("overrideVisible"))))
' <<<"$ALICE_T2_POST")"
check "$((1 - A2_POST_REL))" "Item 2 (/next/ Alice post-release): Release hold disappears once only findings remain"
check "$A2_POST_OVR" "Item 2 (/next/ Alice post-release): Override review gate… remains visible"

# ==============================================================================
# ITEM 1C: Gate without a review behind it (acceptable=false, no Accept button, no Force merge hint)
# ==============================================================================
echo ""
echo "=== ITEM 1C: Findings gate with no review task behind it ==="
T_NOREV=$(api POST /api/tasks '{"title":"Abandoned review gate","description":"Push README change","repo":"acme/widgets","approved":true,"autoMerge":true}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
cat >"$WORK_DIR/action-${T_NOREV}.json" <<'JSON'
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "cd work && echo 'abandoned-test' >> README.md && git add README.md && git commit -m 'chore: abandoned review test' && git push origin HEAD",
        "description": "pushing branch for no-review findings test"
      }
    },
    {"tool": "comment_on_issue", "args": {"comment": "Pushed."}}
  ]
}
JSON
write_rules <<'JSON'
[
  {"match": "WALK-CODE-REVIEW", "action": {"kind": "nothing_needed", "reason": "clean"}}
]
JSON
T_NOREV_PR=$(wait_pr_on_task "$T_NOREV" 160)
wait_reviews_for_parent "$T_NOREV" 1 160
sleep 1.5
# Inject a LinkReviewFindings record with empty reviewTask (exact shape pathtriggerhold.go:813 writes on abandoned gating review).
store_exec "INSERT INTO task_link (task_id, kind, target, blocks) VALUES ('$T_NOREV', 'review-findings', '{\"gate\":\"review\",\"summary\":\"gating review closed without finishing\",\"at\":\"2026-11-17T01:00:00Z\"}', FALSE);"
NOREV_JSON=$(api GET "/api/tasks/$T_NOREV")
NOREV_ACC=$(python3 -c '
import json,sys
g = {x["id"]: x for x in (json.load(sys.stdin).get("mergeGates") or [])}
rev = g.get("review", {})
print(int(rev.get("state") == "findings" and not bool(rev.get("acceptable"))))
' <<<"$NOREV_JSON")
check "$NOREV_ACC" "Item 1C (API): findings gate with no reviewTask has state=findings & acceptable=false"
NOREV_UI=$(walk_ui inspect-next --url "$DAEMON" --jwt "$(fresh_jwt alice@example.com)" --task "$T_NOREV" --openForceMerge)
echo "No-review gate /next/ UI: $NOREV_UI"
read -r NOREV_NO_BTN NOREV_NO_HINT <<<"$(python3 -c '
import json,sys
d = json.loads(sys.stdin.read())
print(int(len(d.get("acceptButtons",[])) == 0), int(not bool(d.get("forceMerge",{}).get("acceptHintVisible"))))
' <<<"$NOREV_UI")"
check "$NOREV_NO_BTN" "Item 1C (/next/): no Accept findings… button on gate without review behind it"
check "$NOREV_NO_HINT" "Item 1C (/next/): Force merge dialog omits Accept findings hint when no gate is acceptable"

# ==============================================================================
# ITEM 3B: Multi-trigger targeted review title + Empty-title fallback + Override review covers targeted-review
# ==============================================================================
echo ""
echo "=== ITEM 3B: Multi-trigger alphabetical title, empty-title fallback, review override covers targeted-review ==="
write_rules <<'JSON'
[
  {
    "match": "WALK-CODE-REVIEW",
    "action": {"kind": "nothing_needed", "reason": "clean"}
  },
  {
    "match": "Targeted review \\(",
    "action": {
      "kind": "mcp_sequence",
      "calls": [
        {"tool": "add_review_comment", "args": {"path": "auth/token.go", "line": 1, "body": "Multi-trigger finding: rotate signing key"}},
        {"tool": "comment_on_issue", "args": {"comment": "Targeted multi-trigger finding."}}
      ]
    }
  }
]
JSON

T3=$(api POST /api/tasks '{"title":"Rotate payment auth","description":"Touch auth/token.go and payments/charge.go","repo":"acme/widgets","approved":true,"autoMerge":true}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
cat >"$WORK_DIR/action-${T3}.json" <<'JSON'
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "cd work && echo '// auth change' >> auth/token.go && echo '// pay change' >> payments/charge.go && git add auth/token.go payments/charge.go && git commit -m 'feat: rotate payment auth' && git push origin HEAD",
        "description": "pushing multi-trigger PR"
      }
    },
    {"tool": "comment_on_issue", "args": {"comment": "Pushed."}}
  ]
}
JSON
T3_PR=$(wait_pr_on_task "$T3" 160)
wait_reviews_for_parent "$T3" 2 200
sleep 2.0

T3_REV_TITLE=$(api GET "/api/tasks?limit=200" | python3 -c '
import json,sys
pid = sys.argv[1]
tasks = json.load(sys.stdin)
revs = [t["title"] for t in tasks if t.get("generatedFrom") == pid]
print("\n".join(revs))
' "$T3")
echo "T3 review titles:"
echo "$T3_REV_TITLE"
check "$(grep -Fxq "Targeted review (auth, payments): Rotate payment auth" <<<"$T3_REV_TITLE" && echo 1 || echo 0)" \
  "Item 3B: multi-trigger targeted review titled alphabetically 'Targeted review (auth, payments): Rotate payment auth'"

# Override 'review' gate via force-merge and verify it covers 'targeted-review'!
FM_RESP=$(api_as "$(fresh_jwt alice@example.com)" POST "/api/tasks/$T3/force-merge" '{"reason":"Override review covers targeted-review","overrideGates":["review","ci"]}')
FM_CODE=$(tail -n 1 <<<"$FM_RESP")
echo "Force merge T3 with overrideGates=[review,ci]: HTTP $FM_CODE"
T3_POST_FM=$(api GET "/api/tasks/$T3")
TARG_OVERRIDDEN=$(python3 -c '
import json,sys
g = {x["id"]: x["state"] for x in (json.load(sys.stdin).get("mergeGates") or [])}
print(int(g.get("targeted-review") == "overridden"))
' <<<"$T3_POST_FM")
check "$TARG_OVERRIDDEN" "Item 3B: overriding 'review' gate covers 'targeted-review' (state=overridden)"

# Empty-title task fallback: file unapproved, clear title in store, approve, let it push & trigger reviews!
T_EMPTY=$(api POST /api/tasks '{"title":"placeholder-to-clear","description":"Touch payments/charge.go","repo":"acme/widgets","approved":false,"autoMerge":true}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
store_exec "UPDATE task SET title = '' WHERE id = '$T_EMPTY';"
cat >"$WORK_DIR/action-${T_EMPTY}.json" <<'JSON'
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "cd work && echo '// untitled change' >> payments/charge.go && git add payments/charge.go && git commit -m 'fix: untitled payment tweak' && git push origin HEAD",
        "description": "pushing untitled task PR"
      }
    },
    {"tool": "comment_on_issue", "args": {"comment": "Pushed."}}
  ]
}
JSON
api POST "/api/tasks/$T_EMPTY/approve" '{}' >/dev/null
T_EMPTY_PR=$(wait_pr_on_task "$T_EMPTY" 160)
wait_reviews_for_parent "$T_EMPTY" 2 200
sleep 1.5
EMPTY_TITLES=$(api GET "/api/tasks?limit=200" | python3 -c '
import json,sys
pid = sys.argv[1]
tasks = json.load(sys.stdin)
revs = [t["title"] for t in tasks if t.get("generatedFrom") == pid]
print("\n".join(revs))
' "$T_EMPTY")
echo "Empty-title task ($T_EMPTY) review titles:"
echo "$EMPTY_TITLES"
check "$(grep -Fxq "Code review: ${T_EMPTY} (${T_EMPTY_PR})" <<<"$EMPTY_TITLES" && echo 1 || echo 0)" \
  "Item 3C: empty-title task falls back to task ID in 'Code review: ${T_EMPTY} (${T_EMPTY_PR})'"
check "$(grep -Fxq "Targeted review (payments): ${T_EMPTY}" <<<"$EMPTY_TITLES" && echo 1 || echo 0)" \
  "Item 3C: empty-title task falls back to task ID in 'Targeted review (payments): ${T_EMPTY}'"

# ==============================================================================
# ITEM 4: Declared repo#credential & widened repo#write vs plain repo#write {repo}
# ==============================================================================
echo ""
echo "=== ITEM 4: Declared credentials & widened pushes vs plain repo#write {repo} ==="
TMP_DECL=$(mktemp -d)
git clone -q "http://$GHSIM/acme/widgets.git" "$TMP_DECL"
mkdir -p "$TMP_DECL/.grain/schedules" "$TMP_DECL/.grain/templates" "$TMP_DECL/.grain/workflows"

cat >"$TMP_DECL/.grain/templates/release-cred-tmpl.yml" <<'YAML'
name: release-cred-tmpl
title: "Release with named credential"
body: "Release using named GitHub credential."
capabilities:
  - 'https://grain.dev/capabilities/repo#credential={"credential":"release-bot"}'
YAML

cat >"$TMP_DECL/.grain/templates/release-push-tmpl.yml" <<'YAML'
name: release-push-tmpl
title: "Release with widened push"
body: "Push release tags."
capabilities:
  - 'https://grain.dev/capabilities/repo#write={"force":true,"refs":["refs/heads/*","refs/tags/*"]}'
YAML

cat >"$TMP_DECL/.grain/templates/plain-write-tmpl.yml" <<'YAML'
name: plain-write-tmpl
title: "Plain repo write template"
body: "Ordinary repo write/read."
capabilities:
  - capability: "https://grain.dev/capabilities/repo#write"
    with:
      repo: acme/widgets
  - capability: "https://grain.dev/capabilities/repo#read"
    with:
      repo: acme/widgets
YAML

cat >"$TMP_DECL/.grain/schedules/release-cred.yml" <<'YAML'
name: release-cred
template: release-cred-tmpl
base: main
every:
  hours: 24
YAML

cat >"$TMP_DECL/.grain/schedules/release-push.yml" <<'YAML'
name: release-push
template: release-push-tmpl
base: main
every:
  hours: 24
YAML

cat >"$TMP_DECL/.grain/workflows/release-cred-ci.yml" <<'YAML'
name: release-cred-ci
on:
  workflow_dispatch:
jobs:
  cred-job:
    capabilities:
      - 'https://grain.dev/capabilities/repo#credential={"credential":"release-bot"}'
    steps:
      - name: noop
        run: echo "CRED-JOB-RAN"
  widened-job:
    capabilities:
      - 'https://grain.dev/capabilities/repo#write={"force":true,"refs":["refs/heads/*","refs/tags/*"]}'
    steps:
      - name: noop
        run: echo "WIDENED-JOB-RAN"
  plain-job:
    capabilities:
      - capability: "https://grain.dev/capabilities/repo#write"
        with:
          repo: acme/widgets
    steps:
      - name: noop
        run: echo "PLAIN-JOB-RAN"
YAML

git -C "$TMP_DECL" add .grain/
git -C "$TMP_DECL" -c user.name="Walk Decl" -c user.email="decl@example.com" commit -qm "chore: declare credential and push schedules, templates, workflows"
DECL_COMMIT=$(git -C "$TMP_DECL" rev-parse HEAD)
git -C "$TMP_DECL" push -q origin main
rm -rf "$TMP_DECL"

force_declaration_resync
wait_declarations_synced "$DECL_COMMIT"
echo "Declarations synced at $DECL_COMMIT"

# Find declared schedule IDs
SCHED_IDS=$(api GET /api/schedules | python3 -c '
import json,sys
by_title = {s.get("title",""): s["id"] for s in json.load(sys.stdin)}
print(by_title.get("release-cred",""), by_title.get("release-push",""))
')
read -r SCHED_CRED_ID SCHED_PUSH_ID <<<"$SCHED_IDS"
echo "Declared schedules: cred=$SCHED_CRED_ID push=$SCHED_PUSH_ID"

# Fire both schedules -> both should be held (state=proposed, unapproved)
FIRED_CRED=$(api POST "/api/schedules/$SCHED_CRED_ID/run" '{}' | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("taskId",""))')
FIRED_PUSH=$(api POST "/api/schedules/$SCHED_PUSH_ID/run" '{}' | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("taskId",""))')
FIRED_CRED_STATE=$(api GET "/api/tasks/$FIRED_CRED" | python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])')
FIRED_PUSH_STATE=$(api GET "/api/tasks/$FIRED_PUSH" | python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])')
check "$([[ "$FIRED_CRED_STATE" == "proposed" ]] && echo 1 || echo 0)" "Item 4: declared schedule with repo#credential fires held (state=$FIRED_CRED_STATE)"
check "$([[ "$FIRED_PUSH_STATE" == "proposed" ]] && echo 1 || echo 0)" "Item 4: declared schedule with repo#write {refs} fires held (state=$FIRED_PUSH_STATE)"

# Fire a task from plain-write-tmpl vs release-cred-tmpl (as non-person or checking declaration acceptance)
TMPL_IDS=$(api GET /api/templates | python3 -c '
import json,sys
by_name = {t.get("name",""): t["id"] for t in json.load(sys.stdin)}
print(by_name.get("release-cred-tmpl",""), by_name.get("plain-write-tmpl",""), by_name.get("release-note",""))
')
read -r TMPL_CRED_ID TMPL_PLAIN_ID TMPL_RELNOTE_ID <<<"$TMPL_IDS"
echo "Declared templates: cred=$TMPL_CRED_ID plain=$TMPL_PLAIN_ID relnote=$TMPL_RELNOTE_ID"

# Start CI runs for cred-job, widened-job, and plain-job!
CI_CRED_ID=$(api POST /api/ci/runs '{"repo":"acme/widgets","ref":"main","event":"workflow_dispatch","workflow":".grain/workflows/release-cred-ci.yml","jobs":["cred-job"]}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
CI_WIDE_ID=$(api POST /api/ci/runs '{"repo":"acme/widgets","ref":"main","event":"workflow_dispatch","workflow":".grain/workflows/release-cred-ci.yml","jobs":["widened-job"]}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
CI_PLAIN_ID=$(api POST /api/ci/runs '{"repo":"acme/widgets","ref":"main","event":"workflow_dispatch","workflow":".grain/workflows/release-cred-ci.yml","jobs":["plain-job"]}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')

# Wait for CI runs to reach terminal state (cred-job & widened-job fail/refuse on unaccepted declaration, plain-job passes!)
for cid in "$CI_CRED_ID" "$CI_WIDE_ID" "$CI_PLAIN_ID"; do
	for _ in $(seq 120); do
		st=$(api GET "/api/ci/runs/$cid" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')
		[[ "$st" == "completed" || "$st" == "failed" || "$st" == "error" || "$st" == "cancelled" ]] && break
		sleep 0.25
	done
done
CI_CRED_JSON=$(api GET "/api/ci/runs/$CI_CRED_ID")
CI_WIDE_JSON=$(api GET "/api/ci/runs/$CI_WIDE_ID")
CI_PLAIN_JSON=$(api GET "/api/ci/runs/$CI_PLAIN_ID")
echo "CI runs: cred=$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status"),d.get("conclusion"))' <<<"$CI_CRED_JSON") wide=$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status"),d.get("conclusion"))' <<<"$CI_WIDE_JSON") plain=$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status"),d.get("conclusion"))' <<<"$CI_PLAIN_JSON")"

# Check GET /api/declarations
DECLS_JSON=$(api GET /api/declarations)
echo "GET /api/declarations: $DECLS_JSON"
read -r HAS_TMPL_CRED HAS_TMPL_PUSH HAS_CI_CRED HAS_CI_WIDE HAS_PLAIN_HELD <<<"$(python3 -c '
import json,sys
raw = json.loads(sys.stdin.read())
rows = raw if isinstance(raw, list) else (raw.get("declarations") or [])
awaiting = {r["id"]: r for r in rows if r.get("awaiting")}
ids = set(awaiting.keys())
print(
  int(any("release-cred" in i for i in ids)),
  int(any("release-push" in i for i in ids)),
  int(any("cred-job" in i for i in ids)),
  int(any("widened-job" in i for i in ids)),
  int(any("plain-job" in i or "plain-write-tmpl" in i for i in ids)),
)
' <<<"$DECLS_JSON")"
check "$HAS_TMPL_CRED" "Item 4: release-cred declaration awaiting acceptance in /api/declarations"
check "$HAS_TMPL_PUSH" "Item 4: release-push declaration awaiting acceptance in /api/declarations"
check_bug "$HAS_CI_CRED" \
  "BUG-7: CI cred-job recorded under ephemeral sandbox name (c<run>-0) instead of stable job ID .grain/workflows/release-cred-ci.yml:cred-job" \
  "Item 4 (Bug 7 fixed): CI cred-job awaiting acceptance under stable job ID in /api/declarations"
check_bug "$HAS_CI_WIDE" \
  "BUG-7: CI widened-job recorded under ephemeral sandbox name (c<run>-0) instead of stable job ID .grain/workflows/release-cred-ci.yml:widened-job" \
  "Item 4 (Bug 7 fixed): CI widened-job awaiting acceptance under stable job ID in /api/declarations"
check "$((1 - HAS_PLAIN_HELD))" "Item 4: plain repo#write {repo} CI job and template are NOT held in /api/declarations"

PLAIN_CI_OK=$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(int(d.get("conclusion") in ("success","passed") or d.get("status")=="passed"))' <<<"$CI_PLAIN_JSON")
check "$PLAIN_CI_OK" "Item 4: plain-job with repo#write {repo} ran unheld and passed"

# Inspect /next/processes/declarations?repo=acme/widgets in Playwright
DECL_UI=$(walk_ui inspect-declarations --url "$DAEMON" --jwt "$(fresh_jwt alice@example.com)" --repo acme/widgets)
echo "Playwright /next/processes/declarations: $DECL_UI"
check "$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(int(d.get("hasRepoText") and d.get("hasCredentialText") and len(d.get("pageErrors",[]))==0))' <<<"$DECL_UI")" \
  "Item 4 (/next/): /next/processes/declarations?repo=acme/widgets displays awaiting declarations with zero page errors"

# Accept release-cred declaration(s) as Alice, verify re-firing schedule queues approved!
ACCEPT_BODIES=$(python3 -c '
import json,sys
raw = json.loads(sys.stdin.read())
rows = raw if isinstance(raw, list) else (raw.get("declarations") or [])
for r in rows:
    if r.get("awaiting") and "release-cred" in r["id"]:
        print(json.dumps({"id": r["id"], "seen": r["requested"]}))
' <<<"$DECLS_JSON")
FIRST_ACCEPT_BODY=$(head -n 1 <<<"$ACCEPT_BODIES")
# Verify Bob (without secrets/capabilities admin) is refused when accepting!
BOB_DECL_CODE=$(api_as "$(fresh_jwt bob@example.com)" POST /api/declarations/accept "$FIRST_ACCEPT_BODY" | tail -n 1)
check "$([[ "$BOB_DECL_CODE" == "403" ]] && echo 1 || echo 0)" "Item 4: repo-scoped writer refused (403) when accepting repo#credential declaration (got $BOB_DECL_CODE)"

ALICE_DECL_OK=1
while IFS= read -r body; do
	[[ -z "$body" ]] && continue
	code=$(api_as "$(fresh_jwt alice@example.com)" POST /api/declarations/accept "$body" | tail -n 1)
	echo "Alice accept declaration: HTTP $code"
	[[ "$code" == "200" ]] || ALICE_DECL_OK=0
done <<<"$ACCEPT_BODIES"
check "$ALICE_DECL_OK" "Item 4: full-access person accepted release-cred declaration(s)"

# Cancel first proposed firing so overlap doesn't skip, then re-fire release-cred -> now approved!
api POST "/api/tasks/$FIRED_CRED/close" '{}' >/dev/null || true
write_rules <<'JSON'
[
  {"match": "Release using named GitHub credential", "action": {"kind": "nothing_needed", "reason": "accepted schedule ran"}}
]
JSON
FIRED_CRED2=$(api POST "/api/schedules/$SCHED_CRED_ID/run" '{}' | python3 -c 'import json,sys; print(json.load(sys.stdin).get("taskId",""))')
FIRED_CRED2_JSON=$(api GET "/api/tasks/$FIRED_CRED2")
FIRED_CRED2_APPROVED=$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(int(d.get("state") != "proposed" and bool(d.get("approvedBy") or d.get("approvedAt") or d.get("state") in ("queued","running","completed","closed"))))' <<<"$FIRED_CRED2_JSON")
check "$FIRED_CRED2_APPROVED" "Item 4: after person accepts declaration, release-cred schedule fires approved (not held)"

# Also accept widened-job CI declaration as Alice and re-run widened-job!
# Under Bug 7, accepting ci:acme/widgets:c<run>-0 does NOT unblock the next run (which gets a new random sandbox name).
WIDE_DECL_BODY=$(python3 -c '
import json,sys
raw = json.loads(sys.stdin.read())
rows = raw if isinstance(raw, list) else (raw.get("declarations") or [])
for r in rows:
    if r.get("kind") == "ci-job" and any("repo#write" in c for c in (r.get("requested") or [])):
        print(json.dumps({"id": r["id"], "seen": r["requested"]}))
        break
' <<<"$DECLS_JSON")
if [[ -n "$WIDE_DECL_BODY" ]]; then
	WIDE_ACC_CODE=$(api_as "$(fresh_jwt alice@example.com)" POST /api/declarations/accept "$WIDE_DECL_BODY" | tail -n 1)
	echo "Alice accept widened-job CI declaration: HTTP $WIDE_ACC_CODE"
	CI_WIDE2_ID=$(api POST /api/ci/runs '{"repo":"acme/widgets","ref":"main","event":"workflow_dispatch","workflow":".grain/workflows/release-cred-ci.yml","jobs":["widened-job"]}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
	for _ in $(seq 120); do
		st=$(api GET "/api/ci/runs/$CI_WIDE2_ID" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')
		[[ "$st" == "completed" || "$st" == "failed" || "$st" == "error" || "$st" == "cancelled" ]] && break
		sleep 0.25
	done
	CI_WIDE2_JSON=$(api GET "/api/ci/runs/$CI_WIDE2_ID")
	WIDE2_OK=$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(int(d.get("conclusion") in ("success","passed") or d.get("status")=="passed"))' <<<"$CI_WIDE2_JSON")
	echo "CI widened-job re-run after acceptance: $(python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status"),d.get("conclusion"))' <<<"$CI_WIDE2_JSON")"
	check_bug "$WIDE2_OK" \
	  "BUG-7: re-running widened-job after accepting its declaration STILL fails because each run gets a new ephemeral sandbox-name declaration ID" \
	  "Item 4 (Bug 7 fixed): re-running widened-job after accepting its declaration runs and passes"
fi

# ==============================================================================
# ITEM 5: Port drops template-restricted grant, keeps catalogue-disabled grant,
#         and dispatch holds the port on the disabled grant instead of running!
# ==============================================================================
echo ""
echo "=== ITEM 5: Port grant filtering (template-restricted dropped, disabled kept & held) ==="
write_rules <<'JSON'
[
  {"match": "WALK-CODE-REVIEW", "action": {"kind": "nothing_needed", "reason": "clean"}},
  {"match": "Port acme/widgets", "action": {"kind": "comment_on_issue", "comment": "BUG: port should have been held at dispatch!"}}
]
JSON

T5=$(api POST /api/tasks "{\"templateId\":\"$TMPL_RELNOTE_ID\",\"fields\":{\"title\":\"v1.2 notes\"},\"repo\":\"acme/widgets\",\"approved\":true,\"autoMerge\":false}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
cat >"$WORK_DIR/action-${T5}.json" <<'JSON'
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "cd work && printf 'line1: shared\nline2: main-release-v1.2\n' > notes.txt && git add notes.txt && git commit -m 'fix: update release notes for v1.2' && git push origin HEAD",
        "description": "updating notes.txt on main to conflict with candidate"
      }
    },
    {"tool": "comment_on_issue", "args": {"comment": "Pushed release notes."}}
  ]
}
JSON
T5_PR=$(wait_pr_on_task "$T5" 160)
T5_PR_NUM=${T5_PR##*#}
echo "Item 5 source task $T5 (template $TMPL_RELNOTE_ID) opened PR $T5_PR"

# Configure capability catalogue:
# - read-issues restricted to templates: ["release-note"] (should be DROPPED on port)
# - apiScopes.presets.ci-read disabled (enabled: false) (should be KEPT on port and HELD at dispatch!)
api PUT /api/capability-policy '{"policy":{"catalogue":{"inProcess":{"read-issues":{"restrictions":{"templates":["release-note"]}}},"apiScopes":{"presets":{"ci-read":{"enabled":false}}}}}}' >/dev/null

# Merge T5's PR in ghsim -> SyncPullRequests observes merge and files port onto candidate!
curl -fsS -X PUT "http://$GHSIM/repos/acme/widgets/pulls/${T5_PR_NUM}/merge" \
  -H "Content-Type: application/json" -d '{"commit_title":"Merge release notes"}' >/dev/null

PORT_ID=""
for _ in $(seq 160); do
	PORT_ID=$(api GET "/api/tasks?limit=200" | python3 -c '
import json,sys
src = sys.argv[1]
for t in json.load(sys.stdin):
    if t.get("portOf") == src:
        print(t["id"])
        break
' "$T5")
	[[ -n "$PORT_ID" ]] && break
	sleep 0.25
done
check "$([[ -n "$PORT_ID" ]] && echo 1 || echo 0)" "Item 5: port task filed onto paired candidate branch (port=$PORT_ID)"

if [[ -n "$PORT_ID" ]]; then
	# Give dispatcher several ticks to run catalogueHold and record task_grant_wait
	sleep 2.5
	PORT_JSON=$(api GET "/api/tasks/$PORT_ID")
	FILED_TMPL=$(store_query "SELECT template_id FROM task_filed_template WHERE task_id = '$PORT_ID';" || true)
	GRANT_WAIT=$(store_query "SELECT capability || '|' || resource FROM task_grant_wait WHERE task_id = '$PORT_ID';" || true)
	echo "Port task $PORT_ID detail (summary):"
	python3 -c '
import json,sys
d = json.load(sys.stdin)
print(json.dumps({
  "id": d.get("id"),
  "state": d.get("state"),
  "filedTemplateDB": sys.argv[1],
  "sourceTemplateId": d.get("templateId"),
  "capabilities": d.get("capabilities"),
  "grantWaitDB": sys.argv[2],
  "activity": d.get("activity"),
  "attempts": len(d.get("attempts") or []),
  "bodyHasDropNote": "settings/capabilities.yml" in (d.get("description") or "") and "read-issues" in (d.get("description") or ""),
}))
' "$FILED_TMPL" "$GRANT_WAIT" <<<"$PORT_JSON"

	read -r P_NO_TMPL P_SRC_TMPL P_DROPPED_ISSUES P_KEPT_SANDBOX P_DROP_NOTE P_HELD_DISPATCH P_NO_RUNS <<<"$(python3 -c '
import json,sys
d = json.load(sys.stdin)
filed_tmpl = sys.argv[1].strip()
grant_wait = sys.argv[2].strip()
caps = d.get("capabilities") or []
body = d.get("description") or ""
print(
  int(not filed_tmpl),
  int(bool(d.get("templateId"))),
  int(not any("read-issues" in c for c in caps)),
  int(any("ci-read" in c for c in caps)),
  int("settings/capabilities.yml" in body and "read-issues" in body),
  int(bool(grant_wait) and d.get("state") == "queued"),
  int(len(d.get("attempts") or []) == 0),
)
' "$FILED_TMPL" "$GRANT_WAIT" <<<"$PORT_JSON")"
	check "$P_NO_TMPL" "Item 5: port has empty templateId (ad-hoc)"
	check "$P_SRC_TMPL" "Item 5: port records sourceTemplateId for reporting"
	check "$P_DROPPED_ISSUES" "Item 5: port dropped template-restricted read-issues grant"
	check_bug "$P_DROP_NOTE" \
	  "BUG-8: portMechanically overwrote port.Body on cherry-pick conflict, erasing the dropped-grants note" \
	  "Item 5 (Bug 8 fixed): port body keeps dropped read-issues grant and settings/capabilities.yml note after mechanical conflict"
	check "$P_KEPT_SANDBOX" "Item 5: port KEPT catalogue-disabled api#ci-read grant"
	check "$P_HELD_DISPATCH" "Item 5: dispatch held port on catalogue wait (task_grant_wait recorded)"
	check "$P_NO_RUNS" "Item 5: port did NOT run without its disabled grant (0 agent runs)"
fi

if [[ -n "${CAPTURE_SCREENSHOTS_DIR:-}" ]]; then
	walk_ui contact-sheet --dir "$CAPTURE_SCREENSHOTS_DIR"
fi

echo ""
echo "================================================================"
echo "  Summary (STORE=$STORE): PASS=$PASSES FAIL=$FAILS BUG_REPRO=$REPROS"
echo "================================================================"
if [[ "$FAILS" -gt 0 ]]; then
	exit 1
fi
