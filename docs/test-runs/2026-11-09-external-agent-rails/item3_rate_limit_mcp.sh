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

# docs/test-plans/2026-11-09.md §7 item 3: >60 requests/minute with one
# gma_ credential from the compiled CLI and from a stdio `grain mcp`
# session gives rate_limited / exit 1 with a retry hint; a second
# credential is unaffected; `grain mcp` exits 0 (no "grain: EOF") when
# the client goes away mid-call. REUSE=1 reuses a running env.
#
#   item3_rate_limit_mcp.sh [OUT]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
OUT=${1:-$HERE/item3-rate-limit-mcp.out}
if [[ ${REUSE:-} == 1 ]]; then
	WORK=${WORK:-/var/tmp/ext-rails}
	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db" BODY="$WORK/body.json"
	export HOME="$WORK/home" GRAIN_SANDBOX_CLI=0
	unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN
else
	start_env "${EXT_WORK:-}" || exit 2
	trap stop_env EXIT
fi
: >"$OUT"
FAILS=0
check() { # id desc expected-regex got
	local res=PASS
	[[ $4 =~ $3 ]] || { res=FAIL; FAILS=$((FAILS + 1)); }
	printf '%s\t%s\t%s\t%s\n' "$res" "$1" "$2" "${4:0:300}" | sed -E 's/gma_[A-Za-z0-9_]+/gma_REDACTED/g' | tee -a "$OUT"
}
code PUT /api/settings '{"externalAgentApi":true}' >/dev/null
"$GRAIN" -token "$CODE" login "$DAEMON" --name rl1 --issue-token >/dev/null 2>&1
"$GRAIN" -token "$CODE" login "$DAEMON" --name rl2 --issue-token >/dev/null 2>&1
"$GRAIN" -token "$CODE" login "$DAEMON" --name rl3 --issue-token >/dev/null 2>&1
check 3.0 "three contexts minted" 'rl1.*rl2.*rl3' "$(jq -r '.contexts | keys | join(" ")' "$HOME/.config/grain/config.json" 2>/dev/null)"

# CLI: 70 `grain list` calls with rl1 inside a minute.
first=""
for i in $(seq 70); do
	"$GRAIN" -json --context rl1 list >"$WORK/rl.out" 2>"$WORK/rl.err"
	rc=$?
	if [[ $rc != 0 && -z $first ]]; then
		first="$i $rc $(tr '\n' ' ' <"$WORK/rl.err")"
	fi
done
check 3.1 "CLI: the 31st grain list (61st request: each list is two calls) fails" '^31 ' "$first"
check 3.2 "CLI: exit 1" '^[0-9]+ 1 ' "$first"
check 3.3 "CLI: code rate_limited" '"code":"rate_limited"' "$first"
check 3.4 "CLI: a retry hint (Retry-After)" 'retry in [0-9]+s' "$first"
"$GRAIN" -json --context rl2 list >/dev/null 2>"$WORK/rl2.err"
check 3.5 "a second credential (rl2) is unaffected" '^0$' "$?"

# MCP: a stdio session on rl3, 70 list_grain_tasks calls in one pipe.
{
	echo '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"walkthrough","version":"1"}}}'
	echo '{"jsonrpc":"2.0","method":"notifications/initialized"}'
	for i in $(seq 1 70); do
		echo "{\"jsonrpc\":\"2.0\",\"id\":$i,\"method\":\"tools/call\",\"params\":{\"name\":\"list_grain_tasks\",\"arguments\":{\"limit\":1}}}"
	done
} | timeout 120 "$GRAIN" --context rl3 mcp >"$WORK/mcp.out" 2>"$WORK/mcp.err"
rc=$?
check 3.6 "grain mcp: exit 0 once stdin closes after 70 calls" '^0$' "$rc"
check 3.7 "grain mcp: no 'grain: EOF' on stderr" '^$' "$(grep -c 'grain: EOF' "$WORK/mcp.err" | sed 's/^0$//')"
firstmcp=$(jq -c 'select(.result.isError == true) | {id, text: .result.content[0].text}' "$WORK/mcp.out" | head -1)
check 3.8 "grain mcp: a call past 60/min is a tool error naming the limit and when to retry" 'rate limit.*retry in [0-9]+s' "$firstmcp"
check 3.9 "grain mcp: calls before the first refusal (each list_grain_tasks may cost more than one request)" '"id":[0-9]+' "$firstmcp"
"$GRAIN" -json --context rl2 list >/dev/null 2>&1
check 3.10 "rl2 still unaffected after rl3's burst" '^0$' "$?"

# Disconnect mid-call: wait_for_task blocks on an open task; the client
# closes stdin 2s in.
code POST /api/tasks '{"title":"wait target","noRepo":true,"approved":false}' >/dev/null
WT=$(jq -r .id "$BODY")
{
	echo '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"walkthrough","version":"1"}}}'
	echo '{"jsonrpc":"2.0","method":"notifications/initialized"}'
	echo "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"wait_for_task\",\"arguments\":{\"id\":\"$WT\",\"timeout_seconds\":60}}}"
	sleep 2
} | timeout 30 "$GRAIN" --context rl2 mcp >"$WORK/mcp2.out" 2>"$WORK/mcp2.err"
rc=$?
check 3.11 "grain mcp: exit 0 when the client disconnects mid-call" '^0$' "$rc"
check 3.12 "grain mcp: stderr clean on disconnect" '^$' "$(tr '\n' ' ' <"$WORK/mcp2.err")"
{
	echo "first CLI refusal: $first"
	echo "first MCP refusal: $firstmcp"
	echo "mid-call disconnect stderr: $(cat "$WORK/mcp2.err")"
} | sed -E 's/gma_[A-Za-z0-9_]+/gma_REDACTED/g' >>"$OUT"
echo "FAILS=$FAILS" | tee -a "$OUT"
exit "$FAILS"
