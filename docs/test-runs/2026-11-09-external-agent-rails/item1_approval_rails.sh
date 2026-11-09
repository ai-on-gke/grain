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

# docs/test-plans/2026-11-09.md §7 item 1: run-now vs approve for three
# external credential shapes x {no-repo, acme/widgets, acme/other}
# proposed tasks, over REST, POST /api/mcp grain_invoke and the compiled
# `grain approve` / `grain run-now` CLI. Each cell gets a fresh proposed
# task (filed by the access code, so no lineage rail is involved); a
# refused run-now must leave it proposed. REUSE=1 reuses a running env.
#
#   item1_approval_rails.sh [OUT]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
OUT=${1:-$HERE/item1-approval-rails.out}
if [[ ${REUSE:-} == 1 ]]; then
	WORK=${WORK:-/var/tmp/ext-rails}
	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db" BODY="$WORK/body.json"
else
	DAEMON_EXTRA_FLAGS="-repo acme/other" start_env "${EXT_WORK:-}" || exit 2
	trap stop_env EXIT
fi
: >"$OUT"
FAILS=0
check() { # id desc expected-regex got
	local res=PASS
	[[ $4 =~ $3 ]] || { res=FAIL; FAILS=$((FAILS + 1)); }
	printf '%s\t%s\t%s\t%s\n' "$res" "$1" "$2" "${4:0:220}" | sed -E 's/gma_[A-Za-z0-9_]+/gma_REDACTED/g' | tee -a "$OUT"
}
err() { jq -r '.error // empty' "$BODY" 2>/dev/null | tr '\n' ' '; }
mint() { # caller(email|code) body -> token
	if [[ $1 == code ]]; then code POST /api/external-agents/tokens "$2" >/dev/null; else as "$1" POST /api/external-agents/tokens "$2" >/dev/null; fi
	jq -r '.token // empty' "$BODY"
}
code PUT /api/settings '{"externalAgentApi":true}' >/dev/null

# (a) via the access code, (b) via alice's IAP assertion (owned, alice is
# grain/operator), (c) via the access code again.
TA=$(mint code '{"name":"shape-a","scopes":["tasks:read","dispatch:write","tasks:admin"],"repos":["acme/widgets"]}')
TB=$(mint alice@example.com '{"name":"shape-b","scopes":["tasks:read","dispatch:write","tasks:admin"]}')
TC=$(mint code '{"name":"shape-c","scopes":["tasks:read","dispatch:write"]}')
check 0.1 "three gma_ credentials minted" '^gma_.+ gma_.+ gma_.+$' "$TA $TB $TC"

file() { # kind -> id of a fresh proposed task
	case $1 in
	norepo) code POST /api/tasks '{"title":"rails no-repo","noRepo":true,"approved":false}' >/dev/null ;;
	widgets) code POST /api/tasks '{"title":"rails widgets","repo":"acme/widgets","approved":false}' >/dev/null ;;
	other) code POST /api/tasks '{"title":"rails other","repo":"acme/other","approved":false}' >/dev/null ;;
	esac
	jq -r .id "$BODY"
}
state() { code GET "/api/tasks/$1" >/dev/null; jq -r .state "$BODY"; }
mcp_invoke() { # token tool id -> ok|refused
	local r
	r=$(curl -s -X POST -H "Authorization: Bearer $1" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
		-d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"grain_invoke\",\"arguments\":{\"tool\":\"$2\",\"arguments\":{\"id\":\"$3\"},\"reason\":\"walkthrough\"}}}" \
		"$DAEMON/api/mcp" | sed -n 's/^data: //p;/^{/p' | head -1)
	echo "$r" >>"$WORK/mcp.log"
	if [[ $(jq -r '.result.isError // .error.message // false' <<<"$r") == false ]]; then echo ok; else echo refused; fi
}
cli() { # token action id -> ok|refused (exit code in $WORK/cli.rc)
	"$GRAIN" -server "$DAEMON" -token "$1" "$2" -reason walkthrough "$3" >"$WORK/cli.out" 2>&1
	local rc=$?
	echo "$rc $(tr '\n' ' ' <"$WORK/cli.out")" >>"$WORK/cli.log"
	if [[ $rc == 0 ]]; then echo ok; else echo "refused(exit $rc)"; fi
}
rest() { # token action id -> ok|refused
	local st
	st=$(tok "$1" POST "/api/tasks/$3/$2" '{}')
	echo "$st $(body)" >>"$WORK/rest.log"
	if [[ $st == 200 ]]; then echo ok; else echo "refused($st)"; fi
}
# expected[shape.kind]: allow or refuse -- set.Allows("tasks:admin", repo).
declare -A WANT=([a.norepo]=refuse [a.widgets]=allow [a.other]=refuse
	[b.norepo]=allow [b.widgets]=allow [b.other]=allow
	[c.norepo]=refuse [c.widgets]=refuse [c.other]=refuse)
printf '| shape | task | route | approve | run-now | state after refused run-now |\n|---|---|---|---|---|---|\n' >"$HERE/item1-table.md"
for shape in a b c; do
	case $shape in a) T=$TA ;; b) T=$TB ;; c) T=$TC ;; esac
	for kind in norepo widgets other; do
		for route in rest mcp cli; do
			ap=$(file "$kind")
			rn=$(file "$kind")
			case $route in
			rest) ra=$(rest "$T" approve "$ap"); rr=$(rest "$T" run-now "$rn") ;;
			mcp) ra=$(mcp_invoke "$T" api_approve "$ap"); rr=$(mcp_invoke "$T" api_run_now "$rn") ;;
			cli) ra=$(cli "$T" approve "$ap"); rr=$(cli "$T" run-now "$rn") ;;
			esac
			sa=$(state "$ap")
			sr=$(state "$rn")
			want=${WANT[$shape.$kind]}
			[[ $ra == ok ]] && ga=allow || ga=refuse
			[[ $rr == ok ]] && gr=allow || gr=refuse
			check "1.$shape.$kind.$route.approve" "approve $ra -> $sa" "^${want}$" "$ga"
			check "1.$shape.$kind.$route.run-now" "run-now $rr -> $sr" "^${want}$" "$gr"
			check "1.$shape.$kind.$route.agree" "approve and run-now agree" "^${ga}$" "$gr"
			if [[ $gr == refuse ]]; then
				check "1.$shape.$kind.$route.left" "refused run-now leaves task $rn proposed" '^proposed$' "$sr"
				check "1.$shape.$kind.$route.apleft" "refused approve leaves task $ap proposed" '^proposed$' "$sa"
			fi
			printf '| %s | %s | %s | %s | %s | %s |\n' "$shape" "$kind" "$route" "$ra" "$rr" "$([[ $gr == refuse ]] && echo "$sr" || echo "($sr)")" >>"$HERE/item1-table.md"
		done
	done
done
# F1: approve without a reason is refused as destructive; run-now of an
# unapproved task approved it with none until this walkthrough's fix.
for action in approve run-now; do
	id=$(file widgets)
	st=$(curl -s -o "$BODY" -w '%{http_code}' -X POST -H "Authorization: Bearer $TB" -H 'Content-Type: application/json' -d '{}' "$DAEMON/api/tasks/$id/$action")
	check "1.noreason.$action" "REST $action under (b) with no X-Grain-Acting-Reason: 403 asking for one" '^403 .*give a reason' "$st $(err)"
	check "1.noreason.$action.left" "task $id left proposed" '^proposed$' "$(state "$id")"
done
# The stdio `grain mcp` server has no approve or run-now tool at all.
"$GRAIN" -token "$CODE" login "$DAEMON" --name mcpctx --issue-token >/dev/null 2>&1
tools=$(printf '%s\n' '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"walkthrough","version":"1"}}}' \
	'{"jsonrpc":"2.0","method":"notifications/initialized"}' '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' |
	timeout 20 "$GRAIN" --context mcpctx mcp 2>/dev/null | jq -r 'select(.id==1) | [.result.tools[].name] | join(",")')
echo "grain mcp tools: $tools" | tee -a "$OUT"
check 1.mcp.tools "grain mcp offers no approve/run-now tool (nothing to compare)" '^[^ ]+$' "$(grep -v -E 'approve|run_now' <<<"$tools")"
# One refusal's text per shape, as the CLI prints it.
grep -m3 'refused\|cannot\|403' "$WORK/cli.log" | sed -E 's/gma_[A-Za-z0-9_]+/gma_REDACTED/g' | cut -c1-400 >>"$OUT"
echo "FAILS=$FAILS" | tee -a "$OUT"
exit "$FAILS"
