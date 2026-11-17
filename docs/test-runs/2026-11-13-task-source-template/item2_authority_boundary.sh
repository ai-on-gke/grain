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

# §1 item 2: the TemplateID authority boundary across every filing path on a
# live daemon. Each dispatched run (fake-agy.py, through the real mcpserver and
# the real sandbox git proxy) tries `git push origin origin/main:refs/tags/walk-*`
# (release-note.yml's `push:`) and `grain issue list` (github#read-issues,
# restricted to release-note by the capability catalogue). STORE=sqlite|postgres.

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
STORE=${STORE:-sqlite}
WORK_DIR="/tmp/walk-st-item2-${STORE}"
trap 'stop_env "$WORK_DIR"' EXIT

FAILURES=0
fail() { echo "FAIL: $*" >&2; FAILURES=$((FAILURES + 1)); }

REL="repo:template:acme:widgets:release-note"
PLN="repo:template:acme:widgets:plain-note"

probe_cmd() {
	echo "set +e; cd work; git fetch -q origin main; P=\$(git push origin origin/main:refs/tags/$1-{{TASK_ID}} 2>&1); PRC=\$?; I=\$(grain issue list 2>&1); IRC=\$?; echo PROBE_PUSH_RC=\$PRC; echo \"PROBE_PUSH_OUT=\$P\"; echo PROBE_ISSUE_RC=\$IRC; echo \"PROBE_ISSUE_OUT=\$I\""
}
probe_action() {
	jq -n --arg cmd "$(probe_cmd "$1")" '{kind:"mcp_sequence",calls:[
		{tool:"run_command",args:{description:"authority probe",command:$cmd}},
		{tool:"nothing_needed",args:{reason:"authority probe done"}}]}'
}
probe_of() {
	local f="$WORK_DIR/runs/mcp-$1.jsonl" text
	[[ -f $f ]] || { echo "no-run"; return; }
	text=$(tail -n 1 "$f" | jq -r '.outputs[0].text')
	echo "push_rc=$(echo "$text" | sed -n 's/^PROBE_PUSH_RC=//p' | head -1) issue_rc=$(echo "$text" | sed -n 's/^PROBE_ISSUE_RC=//p' | head -1)"
}
push_out_of() { tail -n 1 "$WORK_DIR/runs/mcp-$1.jsonl" | jq -r '.outputs[0].text' | grep -m1 -E 'refusing|rejected|new tag' || true; }

provenance() {
	local label=$1 id=$2 want=$3 want_filed=$4
	local lt dt has_l has_d filed src
	lt=$(api GET "/api/tasks?fields=full" | jq -r --arg id "$id" '.[] | select(.id == $id) | .templateId // ""')
	has_l=$(api GET "/api/tasks?fields=full" | jq --arg id "$id" '.[] | select(.id == $id) | has("templateId")')
	dt=$(api GET "/api/tasks/$id" | jq -r '(.task // .).templateId // ""')
	has_d=$(api GET "/api/tasks/$id" | jq '(.task // .) | has("templateId")')
	filed=$(sql_query "SELECT count(*) FROM task_filed_template WHERE task_id = '$id';")
	src=$(sql_query "SELECT COALESCE((SELECT template_id FROM task_source_template WHERE task_id = '$id'), '');")
	echo "[$label] task=$id list.templateId=${lt:-<omitted>} detail.templateId=${dt:-<omitted>} task_filed_template=$filed task_source_template=${src:-<none>}"
	if [[ -z $want ]]; then
		[[ $has_l == false && $has_d == false && $filed == 0 && -z $src ]] || fail "$label: want templateId omitted"
	else
		[[ $lt == "$want" && $dt == "$want" && $src == "$want" && $filed == "$want_filed" ]] || fail "$label: want templateId=$want filed=$want_filed"
	fi
}
expect_probe() {
	local label=$1 id=$2 want=$3 got
	got=$(probe_of "$id")
	echo "[$label] probe: $got | $(push_out_of "$id")"
	[[ $got == "$want" ]] || fail "$label: probe $got, want $want"
}
refused="push_rc=1 issue_rc=1"
allowed="push_rc=0 issue_rc=0"

echo "=== Item 2 ($STORE) ==="
init_env "$WORK_DIR"
start_ghsim "$WORK_DIR"
start_daemon "$GRAIN" "$WORK_DIR"
api PUT /api/capability-policy '{"policy":{"catalogue":{"inProcess":{"read-issues":{"restrictions":{"templates":["release-note"]}}}}}}' >/dev/null
api PUT /api/repos/acme/widgets/declarations '{"declarations":true}' >/dev/null
for _ in $(seq 50); do api GET /api/templates | jq -e --arg id "$REL" '.[] | select(.id == $id)' >/dev/null && break; sleep 0.2; done
for row in $(api GET /api/declarations | jq -r '.[] | select((.requested | length) > 0) | @base64'); do
	r=$(echo "$row" | base64 -d)
	api POST /api/declarations/accept "$(echo "$r" | jq -c '{id, seen: .requested}')" >/dev/null || true
done
probe_action walk-default >"$WORK_DIR/action-default.json"

echo "--- (a1) person POST /api/tasks {templateId: release-note} ---"
T_A1=$(api POST /api/tasks "{\"templateId\":\"$REL\",\"repo\":\"acme/widgets\"}" | jq -r .id)
A1_CMD="$(probe_cmd walk-a1); printf '# acme/widgets\nline-from-a1\n' > README.md; printf -- '- walk release note a1\n' >> CHANGELOG.md; git -c user.name=Walk -c user.email=walk@example.com commit -qam 'fix: release note and readme'; git push -q origin HEAD"
jq -n --arg cmd "$A1_CMD" '{kind:"mcp_sequence",calls:[
	{tool:"run_command",args:{description:"probe and open a PR",command:$cmd}},
	{tool:"comment_on_issue",args:{comment:"opened the release-note PR"}}]}' >"$WORK_DIR/action-$T_A1.json"
api POST "/api/tasks/$T_A1/approve" '{}' >/dev/null
wait_task_state "$T_A1" "awaiting_submit|completed|awaiting_reply|closed" 60 >/dev/null || fail "a1 did not finish"
provenance a1-person "$T_A1" "$REL" 1
expect_probe a1-person "$T_A1" "$allowed"

echo "--- (a2) schedule firing from release-note ---"
SCHED=$(api POST /api/schedules "{\"repo\":\"acme/widgets\",\"templateId\":\"$REL\",\"recurrence\":{\"kind\":\"daily\",\"timeOfDay\":\"01:00\"},\"overlap\":\"allow\"}" | jq -r .id)
probe_action walk-a2 >"$WORK_DIR/action-default.json"
T_A2=$(api POST "/api/schedules/$SCHED/run" '{}' | jq -r '.taskId // .id // empty')
[[ -n $T_A2 ]] || T_A2=$(sql_query "SELECT task_id FROM task_tag WHERE tag LIKE 'schedule:${SCHED}%' ORDER BY task_id DESC LIMIT 1;")
[[ $(task_state "$T_A2") == proposed ]] && api POST "/api/tasks/$T_A2/approve" '{}' >/dev/null
wait_task_state "$T_A2" "completed|closed|awaiting_reply" 60 >/dev/null || fail "a2 did not finish"
provenance a2-schedule "$T_A2" "$REL" 1
expect_probe a2-schedule "$T_A2" "$allowed"

echo "--- merge (a1)'s PR onto main: (a3) path trigger and (d) port to release ---"
probe_action walk-auto >"$WORK_DIR/action-default.json"
PR=$(sql_query "SELECT target FROM task_link WHERE task_id = '$T_A1' AND kind = 'fixes' LIMIT 1;")
echo "a1 PR: $PR"
curl -sS -X PUT "http://$GHSIM/repos/acme/widgets/pulls/${PR##*#}/merge" -d '{}' >/dev/null
T_PORT="" T_TRIG=""
for _ in $(seq 90); do
	T_PORT=$(sql_query "SELECT task_id FROM task_link WHERE kind = 'port-of' AND target = '$T_A1' LIMIT 1;")
	T_TRIG=$(sql_query "SELECT task_id FROM task_tag WHERE tag = 'path-trigger' LIMIT 1;")
	[[ -n $T_PORT && -n $T_TRIG ]] && break
	sleep 1
done
echo "path-trigger task=$T_TRIG port task=$T_PORT"
if [[ -z $T_TRIG ]]; then fail "a3: no path-trigger task"; else
	[[ $(task_state "$T_TRIG") == proposed ]] && api POST "/api/tasks/$T_TRIG/approve" '{}' >/dev/null
	wait_task_state "$T_TRIG" "completed|closed|awaiting_reply" 60 >/dev/null || fail "a3 did not finish"
	provenance a3-path-trigger "$T_TRIG" "$REL" 1
	expect_probe a3-path-trigger "$T_TRIG" "$allowed"
fi
if [[ -z $T_PORT ]]; then fail "d: no port task"; else
	provenance d-port "$T_PORT" "$REL" 0
	echo "[d-port] grants: $(sql_query "SELECT capability FROM task_grant WHERE task_id = '$T_PORT' ORDER BY capability;" | tr '\n' ' ')"
	if wait_task_state "$T_PORT" "completed|closed|awaiting_reply" 60 >/dev/null; then
		expect_probe d-port "$T_PORT" "$refused"
	else
		fail "d: port never ran; grant wait: $(sql_query "SELECT resource FROM task_grant_wait WHERE task_id = '$T_PORT';")"
	fi
fi

echo "--- (b) propose_task(template_id) and (c) agent POST /api/tasks {templateId} ---"
T_DRV=$(api POST /api/tasks '{"title":"Driver","description":"files proposals","repo":"acme/widgets","capabilities":["https://grain.dev/capabilities/api#tasks:write"]}' | jq -r .id)
API_CMD="set +e; for t in $REL $PLN; do echo \"API \$t: \$(grain api POST /api/tasks --body '{\"templateId\":\"'\$t'\",\"repo\":\"acme/widgets\"}' 2>&1)\"; done"
jq -n --arg rel "$REL" --arg cmd "$API_CMD" '{kind:"mcp_sequence",calls:[
	{tool:"propose_task",args:{template_id:$rel,title:"",body:""}},
	{tool:"run_command",args:{description:"agent API filings",command:$cmd}},
	{tool:"nothing_needed",args:{reason:"driver done"}}]}' >"$WORK_DIR/action-$T_DRV.json"
api POST "/api/tasks/$T_DRV/approve" '{}' >/dev/null
wait_task_state "$T_DRV" "completed|closed|awaiting_reply" 60 >/dev/null || fail "driver did not finish"
tail -n 1 "$WORK_DIR/runs/mcp-$T_DRV.jsonl" | jq -r '.outputs[] | "[driver] \(.tool): \(.text | gsub("\n"; " ") | .[0:400])"'

T_B=$(sql_query "SELECT task_id FROM task_link WHERE kind = 'proposed-by' AND target = '$T_DRV' AND task_id IN (SELECT task_id FROM task_source_template WHERE template_id = '$REL') LIMIT 1;")
if [[ -z $T_B ]]; then fail "b: no proposal filed"; else
	provenance b-propose_task "$T_B" "$REL" 0
	probe_action walk-b >"$WORK_DIR/action-$T_B.json"
	api POST "/api/tasks/$T_B/approve" '{}' >/dev/null
	wait_task_state "$T_B" "completed|closed|awaiting_reply" 60 >/dev/null || fail "b did not finish"
	expect_probe b-propose_task "$T_B" "$refused"
fi

T_C=$(sql_query "SELECT task_id FROM task_source_template WHERE template_id = '$PLN' LIMIT 1;")
if [[ -z $T_C ]]; then fail "c: agent API filing of plain-note failed"; else
	provenance c-agent-api "$T_C" "$PLN" 0
	# Give plain-note the same push rule on main before c runs: a task whose
	# templateId is only provenance must still be refused it.
	TMP=$(mktemp -d)
	git clone -q "$WORK_DIR/widgets.git" "$TMP"
	printf 'push:\n  - refs/tags/walk-*\n' >>"$TMP/.grain/templates/plain-note.yml"
	git -C "$TMP" -c user.name=Ops -c user.email=ops@example.com commit -qam "feat: plain-note pushes walk tags"
	git -C "$TMP" push -q origin main
	rm -rf "$TMP"
	for _ in $(seq 60); do
		[[ $(sql_query "SELECT count(*) FROM repo_declaration_push WHERE id = '$PLN';") -ge 1 ]] && break
		sleep 1
	done
	echo "[c] plain-note push rules now declared: $(sql_query "SELECT pattern FROM repo_declaration_push WHERE id = '$PLN';")"
	probe_action walk-c >"$WORK_DIR/action-$T_C.json"
	api POST "/api/tasks/$T_C/approve" '{}' >/dev/null
	wait_task_state "$T_C" "completed|closed|awaiting_reply" 60 >/dev/null || fail "c did not finish"
	expect_probe c-agent-api "$T_C" "$refused"
fi

echo "--- (e) scratch task ---"
T_E=$(api POST /api/tasks '{"title":"Scratch e","description":"no template","repo":"acme/widgets"}' | jq -r .id)
provenance e-scratch "$T_E" "" 0
probe_action walk-e >"$WORK_DIR/action-$T_E.json"
api POST "/api/tasks/$T_E/approve" '{}' >/dev/null
wait_task_state "$T_E" "completed|closed|awaiting_reply" 60 >/dev/null || fail "e did not finish"
expect_probe e-scratch "$T_E" "$refused"

echo "--- edit, reopen and redispatch ---"
api PATCH "/api/tasks/$T_A2" '{"title":"Walk schedule (edited)"}' >/dev/null
provenance a2-after-edit "$T_A2" "$REL" 1
for pair in "b:$T_B:$REL" "c:$T_C:$PLN" "d:$T_PORT:$REL"; do
	IFS=: read -r lbl id tmpl <<<"$pair"
	[[ -z $id ]] && continue
	tmpl=${pair#*:*:}
	api PATCH "/api/tasks/$id" "{\"title\":\"Edited $lbl\",\"description\":\"Edited description $lbl\"}" >/dev/null
	api POST "/api/tasks/$id/reopen" '{"reason":"walk: provenance survives reopen"}' >/dev/null
	probe_action "walk-re-$lbl" >"$WORK_DIR/action-$id.json"
	api POST "/api/tasks/$id/run-now" '{}' >/dev/null
	sleep 2
	wait_task_state "$id" "completed|closed|awaiting_reply" 60 >/dev/null || fail "$lbl redispatch did not finish"
	provenance "$lbl-after-edit-reopen-redispatch" "$id" "$tmpl" 0
	expect_probe "$lbl-redispatch" "$id" "$refused"
done

if ((FAILURES > 0)); then echo "DONE: $FAILURES failure(s) on $STORE"; exit 1; fi
echo "PASS: Item 2 ($STORE)"
