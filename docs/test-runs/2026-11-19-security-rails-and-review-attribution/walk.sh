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

# End-to-end walkthrough for docs/test-plans/2026-11-19.md §7:
#   Suite 1: Cross-task agent halting rail (all 9 halting routes x sandbox-cli & grain-controller MCP)
#   Suite 2: In-place security review commit attribution through real gitproxy (Cases A, B, C, D)
#   Suite 3: trust-boundary/v31 (and HEAD v32..v34) live security-hold triggers + negative control
#
# Run over SQLite (default) or PostgreSQL (STORE=postgres PG_DSN=...).

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

STORE=${STORE:-sqlite}
OUT=${OUT:-$HERE/out/$STORE}
WORK=${WORK:-/home/debian/walk-s7-$STORE}
rm -rf "$OUT"
mkdir -p "$OUT"

cleanup() {
	stop_env "$WORK_DIR" || true
}
trap cleanup EXIT

init_env "$WORK"
echo '{"kind": "nothing_needed", "reason": "walkthrough default completion"}' >"$WORK_DIR/action-default.json"
touch "$WORK_DIR/no-session"
start_ghsim "$WORK_DIR"
start_daemon "$GRAIN" "$WORK_DIR"

log() { echo "== [$(date -u +%H:%M:%S)] [$STORE] $*" | tee -a "$OUT/steps.log"; }

# Use github CI mode so grain doesn't run local CI sandboxes on every PR push,
# while SyncPullRequests still runs clearedForHead on every cycle.
api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"github"}' >/dev/null
api PUT /api/repos/acme/gadgets/ci-mode '{"ciMode":"github"}' >/dev/null

state_of() { api GET "/api/tasks/$1" | jq -r .state; }
wait_state() {
	local id=$1 re=$2 timeout=${3:-240}
	for _ in $(seq "$timeout"); do
		local st
		st=$(state_of "$id")
		[[ "$st" =~ $re ]] && return 0
		sleep 0.25
	done
	echo "TIMEOUT: task $id never reached $re (state=$(state_of "$id"))" >&2
	return 1
}

wait_file() {
	local f=$1 timeout=${2:-240}
	for _ in $(seq "$timeout"); do
		[[ -f "$f" ]] && return 0
		sleep 0.2
	done
	echo "TIMEOUT: file $f never appeared" >&2
	return 1
}

task_links() {
	local id=$1
	sql_exec "SELECT kind || ':' || target FROM task_link WHERE task_id = '$id' ORDER BY kind, target;"
}

wait_security_review() {
	local author_id=$1 prev_id=${2:-} timeout=${3:-240}
	for _ in $(seq "$timeout"); do
		local rev
		rev=$(api GET "/api/tasks/$author_id" | jq -r '.reviewTask // empty')
		if [[ -z "$rev" ]]; then
			rev=$(sql_exec "SELECT target FROM task_link WHERE task_id = '$author_id' AND kind = 'security-review-task' LIMIT 1;")
		fi
		if [[ -n "$rev" && "$rev" != "$prev_id" ]]; then
			echo "$rev"
			return 0
		fi
		sleep 0.25
	done
	echo "TIMEOUT: no security review (prev=$prev_id) filed for task $author_id" >&2
	return 1
}

# =============================================================================
# SUITE 1: Cross-task agent halting rail (all 9 halting routes)
# =============================================================================
log "=== SUITE 1: Cross-task agent halting rail ==="

BLOCKER=$(api POST /api/tasks '{"title":"S1 blocker","repo":"acme/widgets","approved":false,"description":"unapproved blocker"}' | jq -r .id)
HELPER_DEP=$(api POST /api/tasks '{"title":"S1 helper dep","repo":"acme/widgets","approved":false,"description":"dependency target"}' | jq -r .id)

WIDER_PARENT=$(api POST /api/tasks "$(jq -n --arg dep "$BLOCKER" '{
	title: "S1 wider parent",
	repo: "acme/widgets",
	approved: true,
	capabilities: ["https://grain.dev/capabilities/api#tasks:write={\"repo\":\"acme/widgets\"}", "https://grain.dev/capabilities/api#tasks:admin"],
	dependsOn: [$dep],
	description: "Wider parent task holding api:tasks:write@acme/widgets + api:tasks:admin."
}')" | jq -r .id)

WIDER_ADMIN=$(api POST /api/tasks "$(jq -n --arg dep "$BLOCKER" --arg h "$HELPER_DEP" '{
	title: "S1 wider sibling (admin)",
	repo: "acme/widgets",
	approved: true,
	capabilities: ["https://grain.dev/capabilities/api#tasks:write={\"repo\":\"acme/widgets\"}", "https://grain.dev/capabilities/api#tasks:admin"],
	dependsOn: [$dep, $h],
	description: "Wider sibling holding api:tasks:admin."
}')" | jq -r .id)

WIDER_REPO=$(api POST /api/tasks "$(jq -n --arg dep "$BLOCKER" --arg h "$HELPER_DEP" '{
	title: "S1 wider sibling (extra write repo)",
	repo: "acme/widgets",
	approved: true,
	capabilities: ["https://grain.dev/capabilities/api#tasks:write={\"repo\":\"acme/widgets\"}"],
	writes: [{"repo": "acme/gadgets"}],
	dependsOn: [$dep, $h],
	description: "Wider sibling with write access to acme/gadgets."
}')" | jq -r .id)

EQUAL_SIB_CLI=$(api POST /api/tasks "$(jq -n --arg dep "$BLOCKER" '{
	title: "S1 equal-grant sibling (sandbox-cli)",
	repo: "acme/widgets",
	approved: true,
	capabilities: ["https://grain.dev/capabilities/api#tasks:write={\"repo\":\"acme/widgets\"}"],
	dependsOn: [$dep],
	description: "Equal-grant sibling tested via grain api (sandbox-cli)."
}')" | jq -r .id)

EQUAL_SIB_MCP=$(api POST /api/tasks "$(jq -n --arg dep "$BLOCKER" '{
	title: "S1 equal-grant sibling (grain-controller MCP)",
	repo: "acme/widgets",
	approved: true,
	capabilities: ["https://grain.dev/capabilities/api#tasks:write={\"repo\":\"acme/widgets\"}"],
	dependsOn: [$dep],
	description: "Equal-grant sibling tested via grain-controller grain_invoke."
}')" | jq -r .id)

# Acting narrow agent: deferred until children & action script are ready.
ACTING=$(api POST /api/tasks "$(jq -n '{
	title: "S1 acting narrow agent",
	repo: "acme/widgets",
	approved: false,
	capabilities: ["https://grain.dev/capabilities/api#tasks:write={\"repo\":\"acme/widgets\"}"],
	description: "Narrow agent holding only api:tasks:write@acme/widgets."
}')" | jq -r .id)

CHILD_CLI=$(api POST /api/tasks "$(jq -n --arg p "$ACTING" --arg dep "$BLOCKER" '{
	title: "S1 proposed child (sandbox-cli)",
	repo: "acme/widgets",
	approved: true,
	proposedBy: $p,
	capabilities: ["https://grain.dev/capabilities/api#tasks:write={\"repo\":\"acme/widgets\"}"],
	dependsOn: [$dep],
	description: "Child proposed by ACTING, tested via grain api."
}')" | jq -r .id)

CHILD_MCP=$(api POST /api/tasks "$(jq -n --arg p "$ACTING" --arg dep "$BLOCKER" '{
	title: "S1 proposed child (grain-controller MCP)",
	repo: "acme/widgets",
	approved: true,
	proposedBy: $p,
	capabilities: ["https://grain.dev/capabilities/api#tasks:write={\"repo\":\"acme/widgets\"}"],
	dependsOn: [$dep],
	description: "Child proposed by ACTING, tested via grain-controller grain_invoke."
}')" | jq -r .id)

NARROW_CHILD_CLI=$(api POST /api/tasks "$(jq -n --arg dep "$BLOCKER" '{
	title: "S1 narrower child of WIDER_PARENT (sandbox-cli)",
	repo: "acme/widgets",
	approved: true,
	dependsOn: [$dep],
	description: "Narrower child linked via LinkChildOf to WIDER_PARENT."
}')" | jq -r .id)

NARROW_CHILD_MCP=$(api POST /api/tasks "$(jq -n --arg dep "$BLOCKER" '{
	title: "S1 narrower child of WIDER_PARENT (grain-controller MCP)",
	repo: "acme/widgets",
	approved: true,
	dependsOn: [$dep],
	description: "Narrower child linked via LinkChildOf to WIDER_PARENT."
}')" | jq -r .id)

WIDER_CHILD_MCP=$(api POST /api/tasks "$(jq -n --arg dep "$BLOCKER" '{
	title: "S1 wider child of WIDER_PARENT (childOf exemption)",
	repo: "acme/widgets",
	approved: true,
	writes: [{"repo": "acme/gadgets"}],
	dependsOn: [$dep],
	description: "Child with extra write repo linked via LinkChildOf to WIDER_PARENT."
}')" | jq -r .id)

# Add real LinkChildOf rows for WIDER_PARENT's children (blocks=TRUE).
for cid in "$NARROW_CHILD_CLI" "$NARROW_CHILD_MCP" "$WIDER_CHILD_MCP"; do
	sql_exec "INSERT INTO task_link (task_id, kind, target, blocks) VALUES ('$cid', 'child-of', '$WIDER_PARENT', TRUE);"
done

# Give WIDER_PARENT a dependency on HELPER_DEP as well so attach:false has a real target.
api POST "/api/tasks/$WIDER_PARENT/depends-on" "$(jq -n --arg h "$HELPER_DEP" '{id:$h, attach:true}')" >/dev/null

log "S1 IDs: ACTING=$ACTING WIDER_PARENT=$WIDER_PARENT WIDER_ADMIN=$WIDER_ADMIN WIDER_REPO=$WIDER_REPO EQUAL_CLI=$EQUAL_SIB_CLI EQUAL_MCP=$EQUAL_SIB_MCP CHILD_CLI=$CHILD_CLI CHILD_MCP=$CHILD_MCP NARROW_CLI=$NARROW_CHILD_CLI NARROW_MCP=$NARROW_CHILD_MCP WIDER_CHILD=$WIDER_CHILD_MCP HELPER_DEP=$HELPER_DEP"

cat >"$OUT/s1-ids.json" <<EOF
{
  "acting": "$ACTING",
  "wider_parent": "$WIDER_PARENT",
  "wider_admin": "$WIDER_ADMIN",
  "wider_repo": "$WIDER_REPO",
  "equal_sib_cli": "$EQUAL_SIB_CLI",
  "equal_sib_mcp": "$EQUAL_SIB_MCP",
  "child_cli": "$CHILD_CLI",
  "child_mcp": "$CHILD_MCP",
  "narrow_child_cli": "$NARROW_CHILD_CLI",
  "narrow_child_mcp": "$NARROW_CHILD_MCP",
  "wider_child_mcp": "$WIDER_CHILD_MCP",
  "blocker": "$BLOCKER",
  "helper_dep": "$HELPER_DEP"
}
EOF

# Generate action-$ACTING.json and action-$WIDER_PARENT.json via Python so JSON quoting is exact.
python3 - "$WORK_DIR" "$OUT" "$ACTING" "$WIDER_PARENT" "$WIDER_ADMIN" "$WIDER_REPO" \
	"$EQUAL_SIB_CLI" "$EQUAL_SIB_MCP" "$CHILD_CLI" "$CHILD_MCP" \
	"$NARROW_CHILD_CLI" "$NARROW_CHILD_MCP" "$WIDER_CHILD_MCP" "$HELPER_DEP" <<'PY'
import datetime, json, os, sys

(
    work_dir, out_dir, acting, wider_parent, wider_admin, wider_repo,
    equal_cli, equal_mcp, child_cli, child_mcp,
    narrow_cli, narrow_mcp, wider_child_mcp, helper_dep,
) = sys.argv[1:]

future1 = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ")
future2 = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=2)).strftime("%Y-%m-%dT%H:%M:%SZ")

# Order matters for targets where all 11 calls succeed:
# depends-on attach/detach -> wait-on (blocking, then park) -> park-until -> resume-later ->
# defer -> withdraw-approval -> pause -> stop -> close.
ROUTES = [
    ("depends-on-attach", "depends-on", "api_set_dependency", {"id": helper_dep, "attach": True}),
    ("depends-on-detach", "depends-on", "api_set_dependency", {"id": helper_dep, "attach": False}),
    ("wait-on-block",     "wait-on",    "api_wait_on",        {"ids": [helper_dep]}),
    ("wait-on-park",      "wait-on",    "api_wait_on",        {"ids": [helper_dep], "park": True}),
    ("park-until",        "park-until", "api_park_until",     {"until": future1}),
    ("resume-later",      "resume-later", "api_resume_later", {"when": {"at": future2}}),
    ("defer",             "defer",      "api_defer",          {}),
    ("withdraw-approval", "withdraw-approval", "api_withdraw_approval", {}),
    ("pause",             "pause",      "api_pause",          {}),
    ("stop",              "stop",       "api_stop",           {}),
    ("close",             "close",      "api_close",          {}),
]

def build_cli_script(targets, out_jsonl):
    """Write a small python runner in work_dir executed inside the sandbox to invoke `grain api`."""
    specs = []
    for label, tid in targets:
        for rname, path_suffix, _tool, body in ROUTES:
            specs.append({
                "target_label": label,
                "target_id": tid,
                "route": rname,
                "path": f"/api/tasks/{tid}/{path_suffix}",
                "body": json.dumps(body),
            })
    base = os.path.basename(out_jsonl)
    spec_path = os.path.join(work_dir, base + ".specs.json")
    runner_path = os.path.join(work_dir, base + ".runner.py")
    with open(spec_path, "w", encoding="utf-8") as f:
        json.dump(specs, f, indent=2)
    with open(runner_path, "w", encoding="utf-8") as f:
        f.write(
            "import json, subprocess\n"
            f"specs = json.load(open({spec_path!r}))\n"
            "out = []\n"
            "for s in specs:\n"
            "    p = subprocess.run(['grain', 'api', 'POST', s['path'], '--body', s['body']], capture_output=True, text=True)\n"
            "    out.append({**s, 'rc': p.returncode, 'stdout': p.stdout, 'stderr': p.stderr})\n"
            f"open({out_jsonl!r}, 'w').write(json.dumps(out, indent=2))\n"
            "print(f'ran {len(out)} grain api calls')\n"
        )
    return f"python3 {runner_path}"

def build_mcp_calls(targets):
    calls = []
    for label, tid in targets:
        for rname, _path_suffix, tool_name, body in ROUTES:
            calls.append({
                "server": "grain-controller",
                "tool": "grain_invoke",
                "args": {
                    "tool": tool_name,
                    "arguments": {"id": tid, "body": body},
                    "reason": f"s1 walkthrough {label} {rname}",
                },
            })
    return calls

# ACTING action: hold first so walk.sh attaches LinkChildOf(ACTING -> WIDER_PARENT),
# then run sandbox-cli `grain api` calls and grain-controller `grain_invoke` calls.
acting_cli_targets = [
    ("wider_admin", wider_admin),
    ("wider_repo", wider_repo),
    ("wider_parent", wider_parent),
    ("equal_sib_cli", equal_cli),
    ("child_cli", child_cli),
]
acting_mcp_targets = [
    ("wider_admin", wider_admin),
    ("wider_repo", wider_repo),
    ("wider_parent", wider_parent),
    ("equal_sib_mcp", equal_mcp),
    ("child_mcp", child_mcp),
]
acting_action = {
    "kind": "mcp_sequence",
    "hold": True,
    "hold_seconds": 90,
    "calls": [
        {
            "server": "grain-sandbox",
            "tool": "run_command",
            "args": {
                "command": build_cli_script(acting_cli_targets, os.path.join(out_dir, "s1-acting-cli.json")),
                "description": "exercise all 9 halting routes via grain api (sandbox-cli)",
            },
        },
        *build_mcp_calls(acting_mcp_targets),
        {
            "server": "grain-sandbox",
            "tool": "comment_on_issue",
            "args": {"comment": "Suite 1 narrow agent completed all halting-route probes."},
        },
    ],
}
with open(os.path.join(work_dir, f"action-{acting}.json"), "w", encoding="utf-8") as f:
    json.dump(acting_action, f, indent=2)

# WIDER_PARENT action: halt narrower child (both channels) and wider child (childOf exemption).
parent_cli_targets = [("narrow_child_cli", narrow_cli)]
parent_mcp_targets = [
    ("narrow_child_mcp", narrow_mcp),
    ("wider_child_mcp", wider_child_mcp),
]
parent_action = {
    "kind": "mcp_sequence",
    "calls": [
        {
            "server": "grain-sandbox",
            "tool": "run_command",
            "args": {
                "command": build_cli_script(parent_cli_targets, os.path.join(out_dir, "s1-parent-cli.json")),
                "description": "wider parent halting narrower child via grain api (sandbox-cli)",
            },
        },
        *build_mcp_calls(parent_mcp_targets),
        {
            "server": "grain-sandbox",
            "tool": "comment_on_issue",
            "args": {"comment": "Suite 1 wider parent completed child-halting probes."},
        },
    ],
}
with open(os.path.join(work_dir, f"action-{wider_parent}.json"), "w", encoding="utf-8") as f:
    json.dump(parent_action, f, indent=2)
PY

# Approve ACTING, wait for it to start live and hold, attach real LinkChildOf(ACTING -> WIDER_PARENT), then release!
api POST "/api/tasks/$ACTING/approve" '{}' >/dev/null
wait_file "$WORK_DIR/live-$ACTING.hold"
sql_exec "INSERT INTO task_link (task_id, kind, target, blocks) VALUES ('$ACTING', 'child-of', '$WIDER_PARENT', TRUE);"
log "Attached LinkChildOf ($ACTING -> $WIDER_PARENT) while ACTING is live; releasing ACTING"
rm -f "$WORK_DIR/live-$ACTING.hold"
wait_state "$ACTING" '^(completed|awaiting_reply|closed)$'
cp "$WORK_DIR/runs/mcp-$ACTING.jsonl" "$OUT/s1-acting-mcp.jsonl"

# Snapshot WIDER_ADMIN, WIDER_REPO, WIDER_PARENT BEFORE WIDER_PARENT runs (must be untouched!).
api GET "/api/tasks/$WIDER_ADMIN" >"$OUT/s1-wider-admin-after.json"
api GET "/api/tasks/$WIDER_REPO" >"$OUT/s1-wider-repo-after.json"
api GET "/api/tasks/$WIDER_PARENT" >"$OUT/s1-wider-parent-after.json"
task_links "$WIDER_ADMIN" >"$OUT/s1-wider-admin-links.txt"
task_links "$WIDER_REPO" >"$OUT/s1-wider-repo-links.txt"
task_links "$WIDER_PARENT" >"$OUT/s1-wider-parent-links.txt"

# Now remove BLOCKER and HELPER_DEP from WIDER_PARENT so WIDER_PARENT dispatches live and halts its children!
api POST "/api/tasks/$WIDER_PARENT/depends-on" "$(jq -n --arg b "$BLOCKER" '{id:$b, attach:false}')" >/dev/null
api POST "/api/tasks/$WIDER_PARENT/depends-on" "$(jq -n --arg h "$HELPER_DEP" '{id:$h, attach:false}')" >/dev/null
wait_state "$WIDER_PARENT" '^(completed|awaiting_reply|closed)$'
cp "$WORK_DIR/runs/mcp-$WIDER_PARENT.jsonl" "$OUT/s1-parent-mcp.jsonl"
log "Suite 1 complete; verifying assertions"

# =============================================================================
# SUITE 2: In-place security review commit attribution through real gitproxy
# =============================================================================
log "=== SUITE 2: In-place security review commit attribution ==="

# Helper: run an author task that touches pkg/gitproxy/proxy.go with autoMerge=true.
spawn_tb_author() {
	local tag=$1 msg=$2
	local tid
	tid=$(api POST /api/tasks "$(jq -n --arg t "S2-$tag author change" --arg d "Touch pkg/gitproxy/proxy.go for $tag." '{
		title: $t, repo: "acme/widgets", approved: false, autoMerge: true, description: $d
	}')" | jq -r .id)
	cat >"$WORK_DIR/action-$tid.json" <<EOF
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "cd work && echo '// $tag H0' >> pkg/gitproxy/proxy.go && git add pkg/gitproxy/proxy.go && git commit -m '$msg' && git push origin HEAD:{branch}",
        "description": "pushing $tag H0 trust-boundary commit"
      }
    },
    {
      "tool": "comment_on_issue",
      "args": {"comment": "Pushed $tag H0."}
    }
  ]
}
EOF
	api POST "/api/tasks/$tid/approve" '{}' >/dev/null
	echo "$tid"
}

push_human_commit() {
	local br=$1 file=$2 line=$3 msg=$4
	local tmp sha
	tmp=$(mktemp -d)
	git clone -q -b "$br" "$WORK_DIR/widgets.git" "$tmp/w"
	echo "$line" >>"$tmp/w/$file"
	git -C "$tmp/w" add "$file"
	git -C "$tmp/w" -c user.name="Human Alice" -c user.email="alice@example.com" commit -qm "$msg"
	git -C "$tmp/w" push -q origin "$br"
	sha=$(git -C "$tmp/w" rev-parse HEAD)
	rm -rf "$tmp"
	echo "$sha"
}

# --- Case A: outside human commit H1 while review runs; review pushes nothing ---
log "Suite 2 Case A: outside human commit H1, review pushes nothing"
cat >"$WORK_DIR/rules.json" <<EOF
[
  {
    "match": "S2-CASEA author change",
    "actions": [
      {
        "kind": "mcp_sequence",
        "calls": [
          {"tool": "_signal_and_wait", "args": {"touch": "$WORK_DIR/caseA-ready", "wait": "$WORK_DIR/caseA-go"}},
          {"tool": "comment_on_issue", "args": {"comment": "Clean review (pushed nothing)."}}
        ]
      },
      {
        "kind": "comment_on_issue",
        "hold": true,
        "hold_seconds": 90,
        "comment": "Follow-up review of H1 complete."
      }
    ]
  }
]
EOF

AUT_A=$(spawn_tb_author "CASEA" "feat: caseA H0 trust boundary")
wait_state "$AUT_A" '^(completed|awaiting_submit)$'
REV_A1=$(wait_security_review "$AUT_A")
BR_A=$(api GET "/api/tasks/$AUT_A" | jq -r .branch)
H0_A=$(git -C "$WORK_DIR/widgets.git" rev-parse "refs/heads/$BR_A")
log "Case A: AUT_A=$AUT_A BR_A=$BR_A H0=$H0_A REV_A1=$REV_A1"

wait_file "$WORK_DIR/caseA-ready"
H1_A=$(push_human_commit "$BR_A" "pkg/gitproxy/proxy.go" "// caseA human H1" "human: caseA H1 outside commit")
# Seed green check on H1_A to prove it STILL does not auto-merge!
curl -sf -X POST "http://$GHSIM/_sim/check-runs" -d "$(jq -n --arg ref "$H1_A" '{ref:$ref, checks:[{name:"ci", status:"completed", conclusion:"success"}]}')" >/dev/null
log "Case A: human pushed H1=$H1_A while REV_A1 is live; releasing REV_A1"
touch "$WORK_DIR/caseA-go"

wait_state "$REV_A1" '^closed$'
REV_A2=$(wait_security_review "$AUT_A" "$REV_A1")
wait_file "$WORK_DIR/live-$REV_A2.hold"
log "Case A: new review filed and held for H1: REV_A2=$REV_A2"

api GET "/api/tasks/$AUT_A" >"$OUT/s2-caseA-author.json"
api GET "/api/tasks/$REV_A1" >"$OUT/s2-caseA-rev1.json"
api GET "/api/tasks/$REV_A2" >"$OUT/s2-caseA-rev2.json"
task_links "$REV_A1" >"$OUT/s2-caseA-rev1-links.txt"
task_links "$AUT_A" >"$OUT/s2-caseA-author-links.txt"
curl -sS "http://$GHSIM/_sim/pulls" >"$OUT/s2-caseA-pulls.json"
printf '{"aut":"%s","branch":"%s","h0":"%s","h1":"%s","rev1":"%s","rev2":"%s"}\n' \
	"$AUT_A" "$BR_A" "$H0_A" "$H1_A" "$REV_A1" "$REV_A2" >"$OUT/s2-caseA-meta.json"
rm -f "$WORK_DIR/live-$REV_A2.hold"
wait_state "$REV_A2" '^(closed|completed|awaiting_reply)$'

# --- Case B: human pushes H1 under review's fix R1, then H2 on top of R1 ---
log "Suite 2 Case B: human pushes H1 under R1 and H2 over R1"
cat >"$WORK_DIR/rules.json" <<EOF
[
  {
    "match": "S2-CASEB author change",
    "actions": [
      {
        "kind": "mcp_sequence",
        "calls": [
          {"tool": "_signal_and_wait", "args": {"touch": "$WORK_DIR/caseB-ready1", "wait": "$WORK_DIR/caseB-go1"}},
          {
            "tool": "run_command",
            "args": {
              "command": "cd work && git fetch origin && git rebase origin/{branch} && echo '// caseB R1 review fix' >> pkg/widgets/validate.go && git add pkg/widgets/validate.go && git commit -m 'review: caseB R1 fix' && git push origin HEAD:{branch}",
              "description": "rebase onto H1 and push R1 fix through gitproxy"
            }
          },
          {"tool": "_signal_and_wait", "args": {"touch": "$WORK_DIR/caseB-ready2", "wait": "$WORK_DIR/caseB-go2"}},
          {"tool": "comment_on_issue", "args": {"comment": "Pushed R1 validation fix; clean review."}}
        ]
      },
      {
        "kind": "comment_on_issue",
        "hold": true,
        "hold_seconds": 90,
        "comment": "Follow-up review of H2 complete."
      }
    ]
  }
]
EOF

AUT_B=$(spawn_tb_author "CASEB" "feat: caseB H0 trust boundary")
wait_state "$AUT_B" '^(completed|awaiting_submit)$'
REV_B1=$(wait_security_review "$AUT_B")
BR_B=$(api GET "/api/tasks/$AUT_B" | jq -r .branch)
H0_B=$(git -C "$WORK_DIR/widgets.git" rev-parse "refs/heads/$BR_B")
log "Case B: AUT_B=$AUT_B BR_B=$BR_B H0=$H0_B REV_B1=$REV_B1"

wait_file "$WORK_DIR/caseB-ready1"
H1_B=$(push_human_commit "$BR_B" "lib/f1.go" "// caseB human H1 under R1" "human: caseB H1 under review fix")
log "Case B: human pushed H1=$H1_B; releasing REV_B1 to rebase & push R1"
touch "$WORK_DIR/caseB-go1"

wait_file "$WORK_DIR/caseB-ready2"
R1_B=$(git -C "$WORK_DIR/widgets.git" rev-parse "refs/heads/$BR_B")
task_links "$REV_B1" >"$OUT/s2-caseB-rev1-links-midrun.txt"
log "Case B: review pushed R1=$R1_B; now human pushes H2 on top while REV_B1 is still live"
H2_B=$(push_human_commit "$BR_B" "pkg/gitproxy/proxy.go" "// caseB human H2 over R1" "human: caseB H2 over review fix")
log "Case B: human pushed H2=$H2_B; releasing REV_B1 to finish"
touch "$WORK_DIR/caseB-go2"

wait_state "$REV_B1" '^closed$'
REV_B2=$(wait_security_review "$AUT_B" "$REV_B1")
wait_file "$WORK_DIR/live-$REV_B2.hold"
log "Case B: new review filed and held for H2: REV_B2=$REV_B2"

api GET "/api/tasks/$AUT_B" >"$OUT/s2-caseB-author.json"
api GET "/api/tasks/$REV_B1" >"$OUT/s2-caseB-rev1.json"
api GET "/api/tasks/$REV_B2" >"$OUT/s2-caseB-rev2.json"
task_links "$REV_B1" >"$OUT/s2-caseB-rev1-links.txt"
task_links "$AUT_B" >"$OUT/s2-caseB-author-links.txt"
cp "$WORK_DIR/runs/mcp-$REV_B1.jsonl" "$OUT/s2-caseB-rev1-mcp.jsonl"
printf '{"aut":"%s","branch":"%s","h0":"%s","h1":"%s","r1":"%s","h2":"%s","rev1":"%s","rev2":"%s"}\n' \
	"$AUT_B" "$BR_B" "$H0_B" "$H1_B" "$R1_B" "$H2_B" "$REV_B1" "$REV_B2" >"$OUT/s2-caseB-meta.json"
rm -f "$WORK_DIR/live-$REV_B2.hold"
wait_state "$REV_B2" '^(closed|completed|awaiting_reply)$'

# --- Case C: non-fast-forward push refused by gitproxy + remote-rejected push by review ---
log "Suite 2 Case C: non-fast-forward refusal by gitproxy + remote-rejected push by review"
cat >"$WORK_DIR/rules.json" <<EOF
[
  {
    "match": "S2-CASEC author change",
    "actions": [
      {
        "kind": "mcp_sequence",
        "calls": [
          {
            "tool": "run_command",
            "args": {
              "command": "cd work && ORIG=\$(git rev-parse HEAD) && git checkout -q --detach HEAD~1 && echo '// caseC non-ff rewrite' >> pkg/widgets/validate.go && git add pkg/widgets/validate.go && git commit -m 'review: caseC non-ff rewrite' && git rev-parse HEAD > $WORK_DIR/caseC-nonff-sha && (git push --force origin HEAD:{branch} > $WORK_DIR/caseC-nonff-push.log 2>&1 || true) && git checkout -q \$ORIG",
              "description": "attempt non-fast-forward force-push (must be refused by gitproxy)"
            }
          },
          {"tool": "_signal_and_wait", "args": {"touch": "$WORK_DIR/caseC-ready1", "wait": "$WORK_DIR/caseC-go1"}},
          {
            "tool": "run_command",
            "args": {
              "command": "cd work && echo '// caseC rejected fix' >> pkg/widgets/validate.go && git add pkg/widgets/validate.go && git commit -m 'review: caseC rejected fix' && git rev-parse HEAD > $WORK_DIR/caseC-rrej-sha && (git push origin HEAD:{branch} > $WORK_DIR/caseC-hook-push.log 2>&1 || true)",
              "description": "attempt fast-forward push that remote pre-receive hook rejects"
            }
          },
          {"tool": "_signal_and_wait", "args": {"touch": "$WORK_DIR/caseC-ready2", "wait": "$WORK_DIR/caseC-go2"}},
          {"tool": "comment_on_issue", "args": {"comment": "Review finished after non-ff refusal and remote rejected push."}}
        ]
      },
      {
        "kind": "comment_on_issue",
        "hold": true,
        "hold_seconds": 90,
        "comment": "Follow-up review of H_after complete."
      }
    ]
  }
]
EOF

AUT_C=$(spawn_tb_author "CASEC" "feat: caseC H0 trust boundary")
wait_state "$AUT_C" '^(completed|awaiting_submit)$'
REV_C1=$(wait_security_review "$AUT_C")
BR_C=$(api GET "/api/tasks/$AUT_C" | jq -r .branch)
H0_C=$(git -C "$WORK_DIR/widgets.git" rev-parse "refs/heads/$BR_C")
log "Case C: AUT_C=$AUT_C BR_C=$BR_C H0=$H0_C REV_C1=$REV_C1"

wait_file "$WORK_DIR/caseC-ready1"
RNONFF_C=$(tr -d '[:space:]' <"$WORK_DIR/caseC-nonff-sha")
cp "$WORK_DIR/caseC-nonff-push.log" "$OUT/s2-caseC-nonff-push.log"
task_links "$REV_C1" >"$OUT/s2-caseC-rev1-links-after-nonff.txt"
cat >"$WORK_DIR/widgets.git/hooks/pre-receive" <<'HOOK'
#!/usr/bin/env bash
echo "walkthrough pre-receive: rejecting review push" >&2
exit 1
HOOK
chmod +x "$WORK_DIR/widgets.git/hooks/pre-receive"
log "Case C: non-ff push refused ($RNONFF_C); installed rejecting pre-receive hook; releasing REV_C1 to push"
touch "$WORK_DIR/caseC-go1"

wait_file "$WORK_DIR/caseC-ready2"
rm -f "$WORK_DIR/widgets.git/hooks/pre-receive"
RREJ_C=$(tr -d '[:space:]' <"$WORK_DIR/caseC-rrej-sha")
cp "$WORK_DIR/caseC-hook-push.log" "$OUT/s2-caseC-hook-push.log"
BR_C_AFTER_REJ=$(git -C "$WORK_DIR/widgets.git" rev-parse "refs/heads/$BR_C")
task_links "$REV_C1" >"$OUT/s2-caseC-rev1-links-midrun.txt"
log "Case C: push rejected (R_rej=$RREJ_C, branch still=$BR_C_AFTER_REJ); pushing H_after so recordReviewerHead checks reviewer-push links"
HAFTER_C=$(push_human_commit "$BR_C" "pkg/gitproxy/proxy.go" "// caseC human H_after" "human: caseC H_after after rejected push")
touch "$WORK_DIR/caseC-go2"

wait_state "$REV_C1" '^closed$'
REV_C2=$(wait_security_review "$AUT_C" "$REV_C1")
wait_file "$WORK_DIR/live-$REV_C2.hold"
log "Case C: new review filed and held for H_after: REV_C2=$REV_C2"

api GET "/api/tasks/$AUT_C" >"$OUT/s2-caseC-author.json"
api GET "/api/tasks/$REV_C1" >"$OUT/s2-caseC-rev1.json"
api GET "/api/tasks/$REV_C2" >"$OUT/s2-caseC-rev2.json"
task_links "$REV_C1" >"$OUT/s2-caseC-rev1-links.txt"
cp "$WORK_DIR/runs/mcp-$REV_C1.jsonl" "$OUT/s2-caseC-rev1-mcp.jsonl"
printf '{"aut":"%s","branch":"%s","h0":"%s","r_nonff":"%s","r_rej":"%s","branch_after_rej":"%s","h_after":"%s","rev1":"%s","rev2":"%s"}\n' \
	"$AUT_C" "$BR_C" "$H0_C" "$RNONFF_C" "$RREJ_C" "$BR_C_AFTER_REJ" "$HAFTER_C" "$REV_C1" "$REV_C2" >"$OUT/s2-caseC-meta.json"
rm -f "$WORK_DIR/live-$REV_C2.hold"
wait_state "$REV_C2" '^(closed|completed|awaiting_reply)$'

# --- Case D: review pushes R1, then outside force-push rewrites history without R1 ---
log "Suite 2 Case D: review pushes R1, then outside force-push rewrites branch without R1"
cat >"$WORK_DIR/rules.json" <<EOF
[
  {
    "match": "S2-CASED author change",
    "actions": [
      {
        "kind": "mcp_sequence",
        "calls": [
          {
            "tool": "run_command",
            "args": {
              "command": "cd work && echo '// caseD R1 review fix' >> pkg/widgets/validate.go && git add pkg/widgets/validate.go && git commit -m 'review: caseD R1 fix' && git push origin HEAD:{branch}",
              "description": "push R1 fix through gitproxy before outside force-push"
            }
          },
          {"tool": "_signal_and_wait", "args": {"touch": "$WORK_DIR/caseD-ready", "wait": "$WORK_DIR/caseD-go"}},
          {"tool": "comment_on_issue", "args": {"comment": "Review finished after outside force-push."}}
        ]
      },
      {
        "kind": "comment_on_issue",
        "hold": true,
        "hold_seconds": 90,
        "comment": "Follow-up review of H_fp complete."
      }
    ]
  }
]
EOF

AUT_D=$(spawn_tb_author "CASED" "feat: caseD H0 trust boundary")
wait_state "$AUT_D" '^(completed|awaiting_submit)$'
REV_D1=$(wait_security_review "$AUT_D")
BR_D=$(api GET "/api/tasks/$AUT_D" | jq -r .branch)
H0_D=$(git -C "$WORK_DIR/widgets.git" rev-parse "refs/heads/$BR_D")
log "Case D: AUT_D=$AUT_D BR_D=$BR_D H0=$H0_D REV_D1=$REV_D1"

wait_file "$WORK_DIR/caseD-ready"
R1_D=$(git -C "$WORK_DIR/widgets.git" rev-parse "refs/heads/$BR_D")
task_links "$REV_D1" >"$OUT/s2-caseD-rev1-links-midrun.txt"
# Force-push a brand-new history cut from main that omits R1_D!
tmp=$(mktemp -d)
git clone -q -b main "$WORK_DIR/widgets.git" "$tmp/w"
git -C "$tmp/w" checkout -q -b "$BR_D"
echo "// caseD rewritten without R1" >>"$tmp/w/pkg/gitproxy/proxy.go"
git -C "$tmp/w" add pkg/gitproxy/proxy.go
git -C "$tmp/w" -c user.name="Human Alice" -c user.email="alice@example.com" commit -qm "human: caseD force-pushed rewrite without R1"
git -C "$tmp/w" push -q --force origin "$BR_D"
HFP_D=$(git -C "$tmp/w" rev-parse HEAD)
rm -rf "$tmp"
log "Case D: review pushed R1=$R1_D, then human force-pushed H_fp=$HFP_D omitting R1; releasing REV_D1"
touch "$WORK_DIR/caseD-go"

wait_state "$REV_D1" '^closed$'
REV_D2=$(wait_security_review "$AUT_D" "$REV_D1")
wait_file "$WORK_DIR/live-$REV_D2.hold"
log "Case D: new review filed and held for H_fp: REV_D2=$REV_D2"

api GET "/api/tasks/$AUT_D" >"$OUT/s2-caseD-author.json"
api GET "/api/tasks/$REV_D1" >"$OUT/s2-caseD-rev1.json"
api GET "/api/tasks/$REV_D2" >"$OUT/s2-caseD-rev2.json"
task_links "$REV_D1" >"$OUT/s2-caseD-rev1-links.txt"
cp "$WORK_DIR/runs/mcp-$REV_D1.jsonl" "$OUT/s2-caseD-rev1-mcp.jsonl"
printf '{"aut":"%s","branch":"%s","h0":"%s","r1":"%s","h_fp":"%s","rev1":"%s","rev2":"%s"}\n' \
	"$AUT_D" "$BR_D" "$H0_D" "$R1_D" "$HFP_D" "$REV_D1" "$REV_D2" >"$OUT/s2-caseD-meta.json"
rm -f "$WORK_DIR/live-$REV_D2.hold"
wait_state "$REV_D2" '^(closed|completed|awaiting_reply)$'

# Hold every Suite 3 security review open until we snapshot author & review state.
cat >"$WORK_DIR/rules.json" <<'EOF'
[
  {
    "match": "S3 ",
    "repeat": true,
    "action": {
      "kind": "comment_on_issue",
      "hold": true,
      "hold_seconds": 90,
      "comment": "Suite 3 security review inspected and released."
    }
  }
]
EOF

# =============================================================================
# SUITE 3: trust-boundary/v31 live hold triggers (+ v32..v34 + negative control)
# =============================================================================
log "=== SUITE 3: trust-boundary/v31 (+ v32..v34) live hold triggers ==="

S3_CASES=(
	"v31-accept-findings|pkg/ui/accept_findings.go|auth-routes|hold"
	"v31-review-findings-accept|pkg/model/review_findings_accept.go|auth-routes|hold"
	"v31-verifier-session|pkg/ui/verifier_session.go|auth-routes|hold"
	"v31-agentd-update|pkg/agentd/update.go|exec-sandbox|hold"
	"v31-grain-agentd-main|cmd/grain-agentd/main.go|exec-sandbox|hold"
	"v31-build-agentd-sh|scripts/build-agentd.sh|release-machinery|hold"
	"v32-merge-gates|pkg/model/merge_gates.go|hold-policy|hold"
	"v33-agent-cross-task|pkg/ui/agent_cross_task.go|auth-routes|hold"
	"v33-notifications|pkg/ui/notifications.go|auth-routes|hold"
	"v34-refs|pkg/ui/refs.go|auth-routes|hold"
	"neg-lib-f2|lib/f2.go|none|clear"
)

echo "[]" >"$OUT/s3-results.json"

for entry in "${S3_CASES[@]}"; do
	IFS='|' read -r key path want_cat expect_mode <<<"$entry"
	tid=$(api POST /api/tasks "$(jq -n --arg t "S3 $key" --arg p "$path" '{
		title: $t, repo: "acme/widgets", approved: false, autoMerge: true,
		description: ("Modify only " + $p + ".")
	}')" | jq -r .id)
	cat >"$WORK_DIR/action-$tid.json" <<EOF
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "cd work && echo '# $key' >> $path && git add $path && git commit -m 'chore: touch $path' && git push origin HEAD:{branch}",
        "description": "push single-file change touching $path"
      }
    },
    {
      "tool": "comment_on_issue",
      "args": {"comment": "Touched only $path."}
    }
  ]
}
EOF
	api POST "/api/tasks/$tid/approve" '{}' >/dev/null
	wait_state "$tid" '^(completed|awaiting_submit)$'
	if [[ "$expect_mode" == "hold" ]]; then
		rev=$(wait_security_review "$tid")
		wait_file "$WORK_DIR/live-$rev.hold"
		rev_json=$(api GET "/api/tasks/$rev")
		aut_json=$(api GET "/api/tasks/$tid")
		hold_link=$(sql_exec "SELECT target FROM task_link WHERE task_id = '$tid' AND kind = 'security-hold' LIMIT 1;")
		human_only_link=$(sql_exec "SELECT target FROM task_link WHERE task_id = '$tid' AND kind = 'human-only-hold' LIMIT 1;")
		cleared_link=$(sql_exec "SELECT target FROM task_link WHERE task_id = '$tid' AND kind = 'trust-boundary-cleared' LIMIT 1;")
		rm -f "$WORK_DIR/live-$rev.hold"
		wait_state "$rev" '^(closed|completed|awaiting_reply)$'
	else
		sleep 2
		rev=""
		rev_json="{}"
		aut_json=$(api GET "/api/tasks/$tid")
		hold_link=$(sql_exec "SELECT target FROM task_link WHERE task_id = '$tid' AND kind = 'security-hold' LIMIT 1;")
		human_only_link=$(sql_exec "SELECT target FROM task_link WHERE task_id = '$tid' AND kind = 'human-only-hold' LIMIT 1;")
		cleared_link=$(sql_exec "SELECT target FROM task_link WHERE task_id = '$tid' AND kind = 'trust-boundary-cleared' LIMIT 1;")
	fi

	jq --arg key "$key" \
	   --arg path "$path" \
	   --arg want_cat "$want_cat" \
	   --arg expect_mode "$expect_mode" \
	   --arg tid "$tid" \
	   --arg rev "$rev" \
	   --arg hold_link "$hold_link" \
	   --arg human_only_link "$human_only_link" \
	   --arg cleared_link "$cleared_link" \
	   --argjson aut "$aut_json" \
	   --argjson rev_obj "$rev_json" \
	   '. + [{
	      key: $key, path: $path, wantCategory: $want_cat, expectMode: $expect_mode,
	      authorTask: $tid, reviewTask: $rev,
	      autoMergeAtCheck: $aut.autoMerge,
	      securityHold: $hold_link,
	      humanOnlyHold: $human_only_link,
	      trustBoundaryCleared: $cleared_link,
	      reviewTitle: ($rev_obj.title // ""),
	      reviewDescription: ($rev_obj.description // "")
	   }]' "$OUT/s3-results.json" >"$OUT/s3-results.json.tmp"
	mv "$OUT/s3-results.json.tmp" "$OUT/s3-results.json"
	log "S3 $key ($path): author=$tid rev=${rev:-none} hold=${hold_link:-none} humanOnly=${human_only_link:-none} autoMerge=$(echo "$aut_json" | jq -r .autoMerge)"
done

grep -E "security hold|trust boundary|reviewer|cannot |refused \{agent|recording none|moved to" "$WORK_DIR/daemon.log" >"$OUT/daemon-filtered.log" || true
python3 "$HERE/verify.py" "$OUT" "$STORE" | tee "$OUT/summary.txt"
log "ALL SUITES FINISHED ($STORE)"
