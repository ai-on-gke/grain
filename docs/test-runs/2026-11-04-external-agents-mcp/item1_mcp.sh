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

# Item 1 walkthrough: POST /api/mcp over real MCP Go SDK client with Bearer gma_
# and X-Grain-Access-Code, plus daemon log redaction checks.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$DIR/env.sh" start
# shellcheck source=/dev/null
source "${WALKTHROUGH_DIR:-/var/tmp/grain-walkthrough-3070}/vars.env"

# 1. Mint a gma_ token as alice@example.com through the simulated IAP proxy,
#    scoped to grain/external-dev on acme/app only.
MINT_JSON="$(curl -fsS -X POST "${IAP_URL}/api/external-agents/tokens" \
  -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
  -H "X-Iapsim-Email: alice@example.com" \
  -H "Content-Type: application/json" \
  -d '{"name":"alice-mcp","scopeSet":"grain/external-dev","repos":["acme/app"],"selfService":true}')"
GMA_TOKEN="$(printf '%s' "$MINT_JSON" | jq -r '.token')"
GMA_ID="$(printf '%s' "$MINT_JSON" | jq -r '.id')"
printf 'Minted token id=%s owner=%s scopes=%s\n' \
  "$GMA_ID" \
  "$(printf '%s' "$MINT_JSON" | jq -r '.owner')" \
  "$(printf '%s' "$MINT_JSON" | jq -c '.resolvedScopes')"

# 2. Seed an unapproved narrow task holding api:tasks:read@acme/app and
#    api:tasks:write@acme/app so X-Grain-Access-Code + X-Grain-Acting-Task
#    exercises allowed vs refused grain_invoke calls under the access-code path.
NARROW_JSON="$(curl -fsS -X POST "${UI_URL}/api/tasks" \
  -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
  -H "Content-Type: application/json" \
  -d '{"title":"Narrow API task for X-Grain-Access-Code MCP test","repo":"acme/app","approved":false,"capabilities":["api:tasks:read@acme/app","api:tasks:write@acme/app"]}')"
NARROW_ID="$(printf '%s' "$NARROW_JSON" | jq -r '.id')"
printf 'Seeded narrow task id=%s state=%s\n' "$NARROW_ID" "$(printf '%s' "$NARROW_JSON" | jq -r '.state')"

# 3. Run the official MCP Go SDK client (StreamableClientTransport over POST /api/mcp).
cp "$DIR/mcp_sdk_walkthrough.go" /var/tmp/mcp-sdk-runner/mcp_sdk_walkthrough.go
(
  cd /var/tmp/mcp-sdk-runner
  go run ./mcp_sdk_walkthrough.go \
    -endpoint "${UI_URL}/api/mcp" \
    -gma-token "${GMA_TOKEN}" \
    -access-code "${ACCESS_CODE}" \
    -narrow-task "${NARROW_ID}" \
    -out "$DIR/item1-jsonrpc.json"
)

# 4. Verify raw daemon log never contains GMA_TOKEN or ACCESS_CODE in cleartext.
if grep -Fq "${GMA_TOKEN}" "${DAEMON_LOG}"; then
  echo "FAIL: raw GMA_TOKEN appeared in daemon.log!" >&2
  exit 1
fi
if grep -Fq "${ACCESS_CODE}" "${DAEMON_LOG}"; then
  echo "FAIL: raw ACCESS_CODE appeared in daemon.log!" >&2
  exit 1
fi
echo "PASS: neither GMA_TOKEN nor ACCESS_CODE appears in raw daemon.log"

# Summarize key assertions from item1-jsonrpc.json
jq '{
  bearerToolsCount: (.bearerGma.steps[] | select(.step == "bearer-gma/tools/list") | .detail.count),
  bearerTools: (.bearerGma.steps[] | select(.step == "bearer-gma/tools/list") | .detail.tools),
  bearerSubmitState: (.bearerGma.steps[] | select(.step == "bearer-gma/grain_submit_task") | .detail.state),
  bearerSubmitApproved: (.bearerGma.steps[] | select(.step == "bearer-gma/grain_submit_task") | .detail.approved),
  bearerGetSummaryBytes: (.bearerGma.steps[] | select(.step == "bearer-gma/grain_get_task(summary)") | .payloadBytes),
  bearerGetFullBytes: (.bearerGma.steps[] | select(.step == "bearer-gma/grain_get_task(full)") | .payloadBytes),
  bearerAllowedInvokeIsError: (.bearerGma.steps[] | select(.step == "bearer-gma/grain_invoke(allowed:api_add_comment)") | .isError),
  bearerRefusedScopeInvoke: (.bearerGma.steps[] | select(.step == "bearer-gma/grain_invoke(refused-scope:api_get_settings)") | {isError, detail}),
  bearerRefusedRepoInvoke: (.bearerGma.steps[] | select(.step == "bearer-gma/grain_invoke(refused-repo:acme/other)") | {isError, detail}),
  xgacWithGmaError: .xGrainAccessCodeWithGma.error,
  xgacCodeSubmitState: (.xGrainAccessCode.steps[] | select(.step == "x-grain-access-code/grain_submit_task") | .detail.state),
  xgacCodeRefusedScopeInvoke: (.xGrainAccessCode.steps[] | select(.step == "x-grain-access-code/grain_invoke(refused-scope:api_get_settings)") | {isError, detail}),
  xgacCodeRefusedRepoInvoke: (.xGrainAccessCode.steps[] | select(.step == "x-grain-access-code/grain_invoke(refused-repo:acme/other)") | {isError, detail}),
  exchangeCount: (.exchanges | length)
}' "$DIR/item1-jsonrpc.json"
