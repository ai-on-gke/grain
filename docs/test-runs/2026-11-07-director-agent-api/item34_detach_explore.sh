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

# §4 items 3 & 4 (MCP half): an external credential holding only
# capabilities:detach (+ tasks:read) on acme/app, over POST /api/mcp and
# REST; then grain_explore_scope's size and hints. Uses the daemon env.sh
# started.
set -uo pipefail
# shellcheck source=/dev/null
source "${WALKTHROUGH_DIR:-/var/tmp/grain-walkthrough-3488}/vars.env"
AC=(-H "X-Grain-Access-Code: ${ACCESS_CODE}" -H "Content-Type: application/json")
READ_ISSUES='https://grain.dev/capabilities/github#read-issues={}'
JOURNAL='https://grain.dev/capabilities/grain#journal={}'

mint() { # mint NAME SCOPES-JSON REPOS-JSON
  curl -sS -X POST "${UI_URL}/api/external-agents/tokens" "${AC[@]}" \
    -d "{\"name\":\"$1\",\"scopes\":$2,\"repos\":$3,\"owner\":\"alice@example.com\"}" | jq -r .token
}
mcp() { # mcp TOKEN METHOD PARAMS-JSON -> the JSON-RPC result/error
  curl -sS -X POST "${UI_URL}/api/mcp" -H "Authorization: Bearer $1" -H "Content-Type: application/json" \
    -H "Accept: application/json, text/event-stream" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$2\",\"params\":$3}" | sed -n 's/^data: //p;/^{/p'
}
tool() { # tool TOKEN NAME ARGS-JSON -> "isError=<b> <first 260 bytes of text>"
  mcp "$1" tools/call "{\"name\":\"$2\",\"arguments\":$3}" |
    jq -r '"isError=\(.result.isError // false) \((.result.content[0].text // .error.message)[0:260])"'
}

echo "## item 3: capabilities:detach credential"
DET="$(mint detacher '["capabilities:detach","tasks:read"]' '["acme/app"]')"
echo "dangerous scopes on the credential: $(curl -sS "${UI_URL}/api/external-agents/tokens" "${AC[@]}" | jq -c '[.[]?|select(.name=="detacher")|.resolvedScopes][0]')"
echo "- tools/list: $(mcp "$DET" tools/list '{}' | jq -c '[.result.tools[].name]')"
echo "- grain_explore_scope {} groups: $(mcp "$DET" tools/call '{"name":"grain_explore_scope","arguments":{}}' | jq -r '.result.content[0].text' | jq -c '[.groups[]?|{group,scopes:[.scopes[]|{scope,dangerous,routeCount}]}]')"
T="$(curl -sS -X POST "${UI_URL}/api/tasks" "${AC[@]}" -d "{\"title\":\"detach target\",\"repo\":\"acme/app\",\"approved\":false,\"capabilities\":[\"${READ_ISSUES}\",\"${JOURNAL//\"/\\\"}\"]}" | jq -r .id)"
echo "target task ${T} grants: $(curl -sS "${UI_URL}/api/tasks/${T}" "${AC[@]}" | jq -c .capabilities)"
echo "- attach over MCP (must be refused):"
tool "$DET" grain_invoke "{\"tool\":\"api_set_capability\",\"arguments\":{\"id\":\"${T}\",\"body\":{\"capability\":\"api:settings:write\",\"attach\":true}},\"reason\":\"walkthrough\"}"
echo "- replace over MCP (must be refused):"
tool "$DET" grain_invoke "{\"tool\":\"api_set_capability\",\"arguments\":{\"id\":\"${T}\",\"body\":{\"grant\":\"${READ_ISSUES}\",\"attach\":false,\"replace\":\"api:settings:write\"}},\"reason\":\"walkthrough\"}"
echo "- detach without a reason (must be refused):"
tool "$DET" grain_invoke "{\"tool\":\"api_set_capability\",\"arguments\":{\"id\":\"${T}\",\"body\":{\"grant\":\"${READ_ISSUES}\",\"attach\":false}}}"
echo "- detach from the Director (task 1, a standing session; must be refused):"
tool "$DET" grain_invoke "{\"tool\":\"api_set_capability\",\"arguments\":{\"id\":\"1\",\"body\":{\"grant\":\"api:self-debug\",\"attach\":false}},\"reason\":\"walkthrough\"}"
echo "- detach read-issues from ${T} with a reason:"
tool "$DET" grain_invoke "{\"tool\":\"api_set_capability\",\"arguments\":{\"id\":\"${T}\",\"body\":{\"grant\":\"${READ_ISSUES}\",\"attach\":false}},\"reason\":\"walkthrough: stale grant\"}"
echo "target task ${T} grants now: $(curl -sS "${UI_URL}/api/tasks/${T}" "${AC[@]}" | jq -c .capabilities)"
echo "- PATCH capabilities over REST (capabilities:admin route; must be refused):"
curl -sS -o /tmp/det.body -w 'HTTP %{http_code} ' -X PATCH "${UI_URL}/api/tasks/${T}" -H "Authorization: Bearer ${DET}" \
  -H "Content-Type: application/json" -H "X-Grain-Acting-Reason: walkthrough" -d '{"capabilities":["api:settings:write"]}'
jq -c . /tmp/det.body
echo "- repo default capabilities superset over REST (must be refused):"
curl -sS -o /tmp/det.body -w 'HTTP %{http_code} ' -X PUT "${UI_URL}/api/repos/acme/app/capabilities" -H "Authorization: Bearer ${DET}" \
  -H "Content-Type: application/json" -H "X-Grain-Acting-Reason: walkthrough" -d "{\"defaultCapabilities\":[\"${READ_ISSUES}\"]}"
jq -c . /tmp/det.body

echo
echo "## item 4: grain_explore_scope size and hints"
OPS="$(mint explorer '["tasks:read","tasks:write","tasks:admin","dispatch:write","ci:read","capabilities:read","settings:read"]' '["acme/app"]')"
for args in '{}' '{"view":"index"}' '{"scope":"all"}' '{"scope":"tasks"}' '{"tool":"approve"}' '{"tool":"aprove"}' \
  '{"tool":"self-debug"}' '{"tool":"priority-dispatch"}' '{"tool":"read-issues"}' '{"tool":"frobnicate"}' \
  '{"scope":"secrets"}' '{"scope":"tsks"}'; do
  out="$(mcp "$OPS" tools/call "{\"name\":\"grain_explore_scope\",\"arguments\":${args}}" | jq -r '.result.content[0].text // .error.message')"
  printf '%-28s %6d bytes  %s\n' "$args" "${#out}" "$(printf '%s' "$out" | jq -r '.hint // .error // .note // empty' 2>/dev/null | tr '\n' ' ' | cut -c1-220)"
  [[ "${#out}" -lt 1100 ]] &&
    printf '    body: %s\n' "$(printf '%s' "$out" | cut -c1-400)"
done
echo "- grain_invoke near-miss: $(tool "$OPS" grain_invoke '{"tool":"api_aprove","arguments":{"id":"1"}}')"
