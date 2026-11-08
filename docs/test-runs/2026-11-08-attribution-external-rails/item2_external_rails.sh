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

# docs/test-plans/2026-11-08.md §4 item 2: gma_ owner_source (schema 126),
# live role capping, `grain access deny` cutoff, the IAP access-code 403
# and the G8 externalApprovalRail, against the real daemon env.sh starts
# (REUSE=1 reuses a running one).
#
#   item2_external_rails.sh [OUT]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
OUT=${1:-$HERE/item2-external-rails.out}
if [[ ${REUSE:-} == 1 ]]; then
	WORK=${WORK:-/var/tmp/attr-rails}
	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db" BODY="$WORK/body.json"
else
	start_env "${ATTR_WORK:-}" || exit 2
	trap stop_env EXIT
fi
: >"$OUT"
FAILS=0
check() { # id desc expected-regex got
	local res=PASS
	[[ $4 =~ $3 ]] || { res=FAIL; FAILS=$((FAILS + 1)); }
	printf '%s\t%s\t%s\t%s\n' "$res" "$1" "$2" "${4:0:220}" | tee -a "$OUT"
}
err() { jq -r '.error // empty' "$BODY" 2>/dev/null | tr '\n' ' '; }
mint() { # caller(email|code) body -> token
	if [[ $1 == code ]]; then code POST /api/external-agents/tokens "$2" >/dev/null; else as "$1" POST /api/external-agents/tokens "$2" >/dev/null; fi
	jq -r '.token // empty' "$BODY"
}
SCOPES='"tasks:read","tasks:write","tasks:admin","dispatch:write"'
row() { sqlite3 "$DB" "select name||'|'||owner||'|'||owner_source from external_agent_token where name='$1'"; }
origin() { sqlite3 "$DB" "select creator||' <- '||origin_actor_kind||':'||origin_actor_id||':'||origin_actor_source||' for '||coalesce(origin_behalf_kind,'')||':'||coalesce(origin_behalf_id,'')||':'||coalesce(origin_behalf_source,'') from task where id='$1'" 2>/dev/null; }

code PUT /api/settings '{"externalAgentApi":true}' >/dev/null
check 0.1 "external agent API on" 'true' "$(code GET /api/auth/whoami >/dev/null; jq -r .externalAgentApi "$BODY")"

## Three credentials, three owner sources.
T1=$(mint alice@example.com "{\"name\":\"t1-alice\",\"scopes\":[$SCOPES],\"repos\":[\"acme/widgets\"]}")
check 1.1 "T1 minted by IAP-verified alice" '^gma_' "$T1"
check 1.2 "T1 row: owner alice, owner_source iap" '^t1-alice\|alice@example.com\|iap$' "$(row t1-alice)"
T2=$(mint code "{\"name\":\"t2-code\",\"scopes\":[$SCOPES],\"repos\":[\"acme/widgets\"]}")
check 1.3 "T2 minted with the access code" '^gma_' "$T2"
check 1.4 "T2 row: no owner" '^t2-code\|\|$' "$(row t2-code)"
T3=$(mint code "{\"name\":\"t3-named\",\"scopes\":[$SCOPES],\"repos\":[\"acme/widgets\"],\"owner\":\"alice@example.com\"}")
check 1.5 "T3 minted with the access code naming alice" '^gma_' "$T3"
check 1.6 "T3 row: owner alice, owner_source unverified (access-code)" '^t3-named\|alice@example.com\|access-code$' "$(row t3-named)"
# Self-service under the access code: unowned, and naming an owner is refused.
st=$(code POST /api/external-agents/tokens '{"name":"ws-self","scopeSet":"grain/external-dev","selfService":true,"owner":"alice@example.com"}')
check 1.7 "self-service mint under the access code naming alice is refused" '^403 .*acts for nobody' "$st $(err)"
HOME_CLI="$WORK/cli-home"
mkdir -p "$HOME_CLI"
# The access code authenticates the login itself (-token); --token-stdin
# would instead store the code (O3 in the report).
out=$(HOME="$HOME_CLI" "$GRAIN" -token "$CODE" login --issue-token --name walk "$DAEMON" 2>&1)
echo "grain login --issue-token: $out" | tee -a "$OUT"
check 1.8 "grain login --issue-token under the access code mints an unowned token" '\|\|$' \
	"$(sqlite3 "$DB" "select name||'|'||owner||'|'||owner_source from external_agent_token where name not in ('t1-alice','t2-code','t3-named') order by created_at desc limit 1")"

for t in 1 2 3; do
	var="T$t"
	tok "${!var}" POST /api/tasks "{\"title\":\"filed by T$t\",\"repo\":\"acme/widgets\"}" >/dev/null
	eval "F$t=\$(jq -r .id \"\$BODY\")"
done
check 1.9 "T1's filing: on_behalf_of alice, source token (OwnerPrincipal of an iap owner_source: Verified)" 'for human:alice@example.com:token$' "$(origin "$F1")"
check 1.10 "T2's filing: on behalf of nobody" 'for ::$' "$(origin "$F2")"
check 1.11 "T3's filing: on-behalf-of alice only with the unverified access-code source" ' for (::|human:alice@example.com:access-code)$' "$(origin "$F3")"
# F1: Creator is alice, which identityTrailers turned into Requested-by: alice@example.com
# (TestIdentityTrailersNameOnlyVerifiedPeopleOrTheOperator now covers it).
echo "INFO	1.12	T3 filing creator (Requested-by source before the F1 fix): $(sqlite3 "$DB" "select creator from task where id='$F3'")" | tee -a "$OUT"
code GET "/api/tasks/$F3" >/dev/null
echo "T3 filing view: $(jq -c '{author,authorKind,authorLabel,owner}' "$BODY")" | tee -a "$OUT"
tok "$T3" GET /api/auth/whoami >/dev/null
echo "T3 whoami: $(body | head -c 300)" | tee -a "$OUT"

## Live demotion of alice: an owned token is held to her current role.
# A token wider than the owner's role is refused outright, naming the
# scope to drop (docs/agent-api.md "A credential never does more than its
# owner's current role allows"); one re-issued within the role works.
mint_alice() { mint alice@example.com "{\"name\":\"$1\",\"scopes\":[$2],\"repos\":[\"acme/widgets\"]}"; }
TW=$(mint_alice t1-write '"tasks:read","tasks:write"')
TR=$(mint_alice t1-read '"tasks:read"')
probe() { # token -> "file:<st> admin:<st> read:<st>"
	local f a r tgt
	f=$(tok "$1" POST /api/tasks '{"title":"probe filing","repo":"acme/widgets"}')
	tgt=$(code POST /api/tasks '{"title":"probe target","repo":"acme/widgets","approved":false}' >/dev/null; jq -r .id "$BODY")
	a=$(tok "$1" POST "/api/tasks/$tgt/approve" '')
	r=$(tok "$1" GET "/api/tasks/$F1")
	echo "file:$f admin:$a read:$r"
}
role_of() { sqlite3 "$DB" "select role from allowed_principal where entry='user:alice@example.com'"; }
check 2.1 "T1 (tasks:admin) as alice=grain/operator" '^file:201 admin:200 read:200$' "$(probe "$T1")"
gacc role --reason walkthrough user:alice@example.com grain/developer >>"$OUT" 2>&1
check 2.2 "grain/developer is not a shipped set (refused, role unchanged)" '^grain/operator$' "$(role_of)"
gacc role --reason walkthrough user:alice@example.com grain/contributor >>"$OUT" 2>&1
check 2.3 "alice demoted to grain/contributor (no restart)" '^grain/contributor$' "$(role_of)"
check 2.4 "T1 (tasks:admin) refused on the very next request" '^file:403 admin:403 read:403$' "$(probe "$T1")"
tok "$T1" GET "/api/tasks/$F1" >/dev/null
check 2.5 "the refusal names the scope and the role" 'wider than its owner.s current role: it grants tasks:admin.*grain/contributor' "$(err)"
check 2.6 "t1-write (within contributor) files, cannot approve" '^file:201 admin:403 read:200$' "$(probe "$TW")"
gacc role --reason walkthrough user:alice@example.com grain/viewer >>"$OUT" 2>&1
check 2.7 "alice demoted to grain/viewer" '^grain/viewer$' "$(role_of)"
check 2.8 "t1-write refused once wider than viewer" '^file:403 admin:403 read:403$' "$(probe "$TW")"
check 2.9 "t1-read (within viewer) reads only" '^file:403 admin:403 read:200$' "$(probe "$TR")"
gacc deny user:alice@example.com >>"$OUT" 2>&1
check 2.10 "t1-read once alice is denied: 403 on everything" '^file:403 admin:403 read:403$' "$(probe "$TR")"
tok "$TR" GET "/api/tasks/$F1" >/dev/null
check 2.11 "the denial says alice holds no role" 'holds no role' "$(err)"
TC=$(mint code "{\"name\":\"t-charlie\",\"scopes\":[$SCOPES],\"repos\":[\"acme/widgets\"],\"owner\":\"charlie@example.com\"}")
st=$(tok "$TC" POST /api/tasks '{"title":"charlie","repo":"acme/widgets"}')
check 2.12 "a token owned by never-listed charlie is refused, even filing" '^403 .*charlie@example.com, who holds no role' "$st $(err)"
st=$(tok "$T2" GET "/api/tasks/$F2")
check 2.13 "the unowned T2 is not refused for being unlisted" '^200$' "$st"
gacc allow --role grain/operator --reason walkthrough user:alice@example.com >>"$OUT" 2>&1
check 2.14 "alice re-allowed as operator: T1 works again, no restart" '^file:201 admin:200 read:200$' "$(probe "$T1")"

## POST /api/auth/access-code carrying alice's verified IAP assertion.
ac() { # code -> "status|sha of body|set-cookie count"
	local h="$WORK/ac.h"
	curl -s -D "$h" -o "$WORK/ac.b" -X POST -H "$IAP_HDR: $(sign_jwt alice@example.com)" -H 'Content-Type: application/json' \
		-d "{\"code\":\"$1\"}" "$DAEMON/api/auth/access-code" >/dev/null
	printf '%s|%s|%s' "$(head -1 "$h" | awk '{print $2}')" "$(sha256sum <"$WORK/ac.b" | cut -c1-12)" "$(grep -ci '^set-cookie' "$h")"
}
good=$(ac "$CODE")
bad=$(ac "WRONG-CODE")
cp "$WORK/ac.b" "$HERE/item2-access-code-403.json"
check 3.1 "right code + alice's assertion: 403, no cookie" '^403\|[0-9a-f]+\|0$' "$good"
check 3.2 "wrong code + alice's assertion: 403, no cookie" '^403\|[0-9a-f]+\|0$' "$bad"
check 3.3 "identical responses (no code-comparison oracle)" "^${good}$" "$bad"

## G8 externalApprovalRail with a tasks:admin gma_ token.
T4=$(mint alice@example.com "{\"name\":\"t4-admin\",\"scopes\":[$SCOPES],\"repos\":[\"acme/widgets\"]}")
tok "$T4" POST /api/tasks '{"title":"P1 filed by t4","repo":"acme/widgets"}' >/dev/null
P1=$(jq -r .id "$BODY")
# P1's run proposes P2 (the scripted agent's real propose_task call): a
# person approves P1 first, as one would before it ran.
as bob@example.com POST "/api/tasks/$P1/approve" '' >/dev/null
propose "$P1" '{"title":"P2 proposed by P1 run","body":"lineage hop 1"}' >>"$OUT"
P2=$(cat "$WORK/proposed.id")
propose "$P1" '{"title":"P2x proposed by P1 run (approved by bob)","body":"lineage hop 1"}' >>"$OUT"
P2X=$(cat "$WORK/proposed.id")
as bob@example.com POST "/api/tasks/$P2X/approve" '' >/dev/null
propose "$P2X" '{"title":"P2b proposed by P2x run","body":"lineage hop 2"}' >>"$OUT"
P2B=$(cat "$WORK/proposed.id")
tok "$T4" POST /api/tasks '{"title":"P1u filed by t4, left unapproved","repo":"acme/widgets"}' >/dev/null
P1U=$(jq -r .id "$BODY")
file_as_alice() { as alice@example.com POST /api/tasks "$1" >/dev/null; jq -r .id "$BODY"; }
# P3: hold_for_approval is propose_task's flag, so a run alice filed and
# approved (outside t4's lineage) proposes it.
HOLDER=$(file_as_alice '{"title":"run that proposes P3","repo":"acme/widgets"}')
propose "$HOLDER" '{"title":"P3 hold_for_approval","body":"a person decides","hold_for_approval":true}' >>"$OUT"
P3=$(cat "$WORK/proposed.id")
check 4.0 "P3 carries hold-requested" 'hold-requested' "$(sqlite3 "$DB" "select tag from task_tag where task_id='$P3'" | tr '\n' ' ')"
P4=$(file_as_alice '{"title":"P4 admission durable","repo":"acme/widgets","approved":false,"admission":"durable"}')
P5=$(file_as_alice '{"title":"P5 auto_merge","repo":"acme/widgets","approved":false,"autoMerge":true}')
U1=$(file_as_alice '{"title":"U1 unrelated standard","repo":"acme/widgets","approved":false}')
U2=$(file_as_alice '{"title":"U2 unrelated standard","repo":"acme/widgets","approved":false}')
U3=$(file_as_alice '{"title":"U3 unrelated standard","repo":"acme/widgets","approved":false}')
echo "P1=$P1 P1U=$P1U HOLDER=$HOLDER P2=$P2 P2X=$P2X P2B=$P2B P3=$P3 P4=$P4 P5=$P5 U1=$U1 U2=$U2 U3=$U3" | tee -a "$OUT"
# P1 is approved already (by bob) -- the rail still answers for run-now.
mcp_invoke() { # token tool id -> "isError|text"
	curl -s -X POST -H "Authorization: Bearer $1" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
		-d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"grain_invoke\",\"arguments\":{\"tool\":\"$2\",\"arguments\":{\"id\":\"$3\"},\"reason\":\"walkthrough\"}}}" \
		"$DAEMON/api/mcp" | sed -n 's/^data: //p;/^{/p' | head -1 | jq -r '"\(.result.isError)|\(.result.content[0].text // .error.message)"' | tr '\n' ' '
}
state() { sqlite3 "$DB" "select approval_actor_kind||':'||approval_actor_id from task where id='$1'" 2>/dev/null; }
for pair in "P1u:$P1U:it filed it" "P2:$P2:it filed task $P1" "P2b:$P2B:$P1 → $P2X" "P3:$P3:hold_for_approval" "P4:$P4:durable" "P5:$P5:auto"; do
	IFS=: read -r name id want <<<"$pair"
	[[ -z $id ]] && continue
	before=$(state "$id")
	for action in approve run-now; do
		st=$(tok "$T4" POST "/api/tasks/$id/$action" '{}')
		check "4.$name.$action" "REST $action $name refused by externalApprovalRail" "^403 .*external:t4-admin.*$want" "$st $(err)"
	done
	for tool in api_approve api_run_now; do
		check "4.$name.$tool" "MCP grain_invoke $tool $name refused" "^true\|.*403.*$want" "$(mcp_invoke "$T4" "$tool" "$id")"
	done
	check "4.$name.unchanged" "$name's approval unchanged" "^${before}$" "$(state "$id")"
done
# P1 itself is approved already (bob): run-now approves nothing, so the
# rail (onlyIfUnapproved) lets it jump the queue; approve is still refused.
st=$(tok "$T4" POST "/api/tasks/$P1/approve" '{}')
check 4.P1.approve "REST approve of the approved P1 refused (its own filing)" '^403 .*it filed it' "$st $(err)"
st=$(tok "$T4" POST "/api/tasks/$P1/run-now" '{}')
check 4.P1.run-now "REST run-now of the approved P1: no approval involved, allowed" '^200 ?$' "$st $(err)"
st=$(tok "$T4" POST "/api/tasks/$U1/approve" '{}')
check 4.U1 "REST approve of an unrelated standard task in scope succeeds" '^200 ?$' "$st $(err)"
st=$(tok "$T4" POST "/api/tasks/$U2/run-now" '{}')
check 4.U2 "REST run-now of an unrelated standard task in scope succeeds" '^200 ?$' "$st $(err)"
check 4.U3 "MCP grain_invoke api_approve of an unrelated standard task succeeds" '^(false|null)\|' "$(mcp_invoke "$T4" api_approve "$U3")"
check 4.U3.state "U3 approved on behalf of alice" 'external' "$(state "$U3")"

echo "FAILS=$FAILS" | tee -a "$OUT"
exit "$FAILS"
