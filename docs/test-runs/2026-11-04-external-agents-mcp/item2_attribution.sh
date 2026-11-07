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

# Item 2 walkthrough: Owner attribution across UI API, CLI, grain task read,
# SQLite store, and daemon journal for tokens self-minted by alice, minted for
# bob by an operator, and minted by an agent (both external-agent and task).
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$DIR/env.sh" start
# shellcheck source=/dev/null
source "${WALKTHROUGH_DIR:-/var/tmp/grain-walkthrough-3070}/vars.env"

OUT_TXT="$DIR/item2-cli-and-journal.txt"
OUT_JSON="$DIR/item2-attribution.json"
: >"$OUT_TXT"

log_section() {
  printf '\n=== %s ===\n' "$1" | tee -a "$OUT_TXT"
}

iap_post() {
  local email="$1" path="$2" body="$3"
  curl -sS -w '\n%{http_code}' -X POST "${IAP_URL}${path}" \
    -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
    -H "X-Iapsim-Email: ${email}" \
    -H "Content-Type: application/json" \
    -d "${body}"
}

split_resp() {
  HTTP_STATUS="$(printf '%s' "$1" | tail -n1)"
  HTTP_BODY="$(printf '%s' "$1" | sed '$d')"
}

log_section "1. Minting tokens (alice self-service, operator for bob, external agent, acting task)"

# 1a. Self-service cross-owner refusal check
split_resp "$(iap_post "alice@example.com" "/api/external-agents/tokens" \
  '{"name":"alice-spoof-bob","scopeSet":"grain/external-dev","owner":"bob@example.com","selfService":true}')"
printf 'alice self-service naming bob -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]

# 1b. Token A: self-minted by alice@example.com
split_resp "$(iap_post "alice@example.com" "/api/external-agents/tokens" \
  '{"name":"alice-self","scopeSet":"grain/external-dev","repos":["acme/app"],"selfService":true}')"
[[ "$HTTP_STATUS" == "201" ]]
ALICE_TOK="$(printf '%s' "$HTTP_BODY" | jq -r '.token')"
ALICE_VIEW="$(printf '%s' "$HTTP_BODY" | jq 'del(.token)')"
printf 'Token A (alice-self): %s\n' "$(printf '%s' "$ALICE_VIEW" | jq -c .)" | tee -a "$OUT_TXT"

# 1c. Token B: minted by operator@example.com for bob@example.com
split_resp "$(iap_post "operator@example.com" "/api/external-agents/tokens" \
  '{"name":"bob-by-op","scopeSet":"grain/external-dev","repos":["acme/app"],"owner":"bob@example.com"}')"
[[ "$HTTP_STATUS" == "201" ]]
BOB_TOK="$(printf '%s' "$HTTP_BODY" | jq -r '.token')"
BOB_VIEW="$(printf '%s' "$HTTP_BODY" | jq 'del(.token)')"
printf 'Token B (bob-by-op): %s\n' "$(printf '%s' "$BOB_VIEW" | jq -c .)" | tee -a "$OUT_TXT"

# 1d. Token C1: minted by an external agent owned by alice (holding capabilities:admin)
split_resp "$(iap_post "operator@example.com" "/api/external-agents/tokens" \
  '{"name":"alice-issuer","scopes":["capabilities:admin","tasks:read","tasks:write"],"owner":"alice@example.com"}')"
[[ "$HTTP_STATUS" == "201" ]]
ALICE_ISSUER_TOK="$(printf '%s' "$HTTP_BODY" | jq -r '.token')"

# Agent tries to widen beyond its own scopes -> must be 403 (scope attenuation)
split_resp "$(curl -sS -w '\n%{http_code}' -X POST "${UI_URL}/api/external-agents/tokens" \
  -H "Authorization: Bearer ${ALICE_ISSUER_TOK}" \
  -H "X-Grain-Acting-Reason: walkthrough item 2 scope widening test" \
  -H "Content-Type: application/json" \
  -d '{"name":"agent-widen","scopeSet":"grain/external-dev"}')"
printf 'external agent widening scopes -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]

# Agent tries to name bob as owner within its own scopes -> must be 403
split_resp "$(curl -sS -w '\n%{http_code}' -X POST "${UI_URL}/api/external-agents/tokens" \
  -H "Authorization: Bearer ${ALICE_ISSUER_TOK}" \
  -H "X-Grain-Acting-Reason: walkthrough item 2 cross-owner test" \
  -H "Content-Type: application/json" \
  -d '{"name":"agent-spoof-bob","scopes":["tasks:read"],"owner":"bob@example.com"}')"
printf 'external agent naming bob -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]

# Agent mints child token within its own scopes -> inherits alice@example.com
split_resp "$(curl -sS -w '\n%{http_code}' -X POST "${UI_URL}/api/external-agents/tokens" \
  -H "Authorization: Bearer ${ALICE_ISSUER_TOK}" \
  -H "X-Grain-Acting-Reason: walkthrough item 2 child token mint" \
  -H "Content-Type: application/json" \
  -d '{"name":"alice-child-agent","scopes":["tasks:read","tasks:write"],"repos":["acme/app"]}')"
[[ "$HTTP_STATUS" == "201" ]]
CHILD_AGENT_TOK="$(printf '%s' "$HTTP_BODY" | jq -r '.token')"
CHILD_AGENT_VIEW="$(printf '%s' "$HTTP_BODY" | jq 'del(.token)')"
printf 'Token C1 (alice-child-agent, minted by external agent): %s\n' "$(printf '%s' "$CHILD_AGENT_VIEW" | jq -c .)" | tee -a "$OUT_TXT"

# 1e. Token C2: minted by an acting task holding api:capabilities:admin, api:tasks:read, api:tasks:write
TASK_ISSUER_JSON="$(curl -fsS -X POST "${UI_URL}/api/tasks" \
  -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
  -H "Content-Type: application/json" \
  -d '{"title":"Task issuer with capabilities:admin","repo":"acme/app","approved":false,"capabilities":["api:capabilities:admin","api:tasks:read","api:tasks:write"]}')"
TASK_ISSUER_ID="$(printf '%s' "$TASK_ISSUER_JSON" | jq -r '.id')"

# Task tries to name alice as owner -> must be 403
split_resp "$(curl -sS -w '\n%{http_code}' -X POST "${UI_URL}/api/external-agents/tokens" \
  -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
  -H "X-Grain-Acting-Task: ${TASK_ISSUER_ID}" \
  -H "X-Grain-Acting-Reason: walkthrough item 2 task spoof owner test" \
  -H "Content-Type: application/json" \
  -d '{"name":"task-laundered","scopes":["tasks:read"],"owner":"alice@example.com"}')"
printf 'acting task naming alice -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]

# Task mints unowned token -> owner=""
split_resp "$(curl -sS -w '\n%{http_code}' -X POST "${UI_URL}/api/external-agents/tokens" \
  -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
  -H "X-Grain-Acting-Task: ${TASK_ISSUER_ID}" \
  -H "X-Grain-Acting-Reason: walkthrough item 2 task mint unowned token" \
  -H "Content-Type: application/json" \
  -d '{"name":"task-minted-bot","scopes":["tasks:read","tasks:write"],"repos":["acme/app"]}')"
[[ "$HTTP_STATUS" == "201" ]]
TASK_MINTED_TOK="$(printf '%s' "$HTTP_BODY" | jq -r '.token')"
TASK_MINTED_VIEW="$(printf '%s' "$HTTP_BODY" | jq 'del(.token)')"
printf 'Token C2 (task-minted-bot, minted by task %s): %s\n' "$TASK_ISSUER_ID" "$(printf '%s' "$TASK_MINTED_VIEW" | jq -c .)" | tee -a "$OUT_TXT"

log_section "2. Filing tasks and comments via grain CLI with GRAIN_CLIENT and GRAIN_CLIENT_SESSION"

file_and_comment_via_cli() {
  local tok="$1" client="$2" session="$3" title="$4" comment_body="$5"
  local create_out id
  create_out="$(GRAIN_SERVER="${UI_URL}" GRAIN_TOKEN="${tok}" GRAIN_CLIENT="${client}" GRAIN_CLIENT_SESSION="${session}" \
    "$GRAIN_BIN" create -repo acme/app -title "${title}" -body "Filed via CLI walkthrough (${client}, ${session}).")"
  printf 'CLI create (%s): %s\n' "$title" "$create_out" >>"$OUT_TXT"
  id="$(printf '%s\n' "$create_out" | awk 'NR==1{print $1}' | tr -d '#:')"
  GRAIN_SERVER="${UI_URL}" GRAIN_TOKEN="${tok}" GRAIN_CLIENT="${client}" GRAIN_CLIENT_SESSION="${session}" \
    "$GRAIN_BIN" comment "$id" "$comment_body" >>"$OUT_TXT"
  printf '%s' "$id"
}

T_ALICE="$(file_and_comment_via_cli "$ALICE_TOK" "claude-code/1.2.0" "sess-alice-101" \
  "Task from alice self-minted token" "Follow-up comment from alice-self CLI")"
T_BOB="$(file_and_comment_via_cli "$BOB_TOK" "codex-cli/0.9.2" "sess-bob-202" \
  "Task from operator-minted bob token" "Follow-up comment from bob-by-op CLI")"
T_CHILD="$(file_and_comment_via_cli "$CHILD_AGENT_TOK" "jetski/2.1" "sess-child-303" \
  "Task from external-agent-minted child token" "Follow-up comment from alice-child-agent CLI")"
T_TASKBOT="$(file_and_comment_via_cli "$TASK_MINTED_TOK" "subagent-runner/0.4" "sess-taskbot-404" \
  "Task from task-minted unowned token" "Follow-up comment from task-minted-bot CLI")"

log_section "3. Body author spoof and X-Forwarded-User spoof checks"

split_resp "$(curl -sS -w '\n%{http_code}' -X POST "${UI_URL}/api/tasks" \
  -H "Authorization: Bearer ${ALICE_TOK}" \
  -H "Content-Type: application/json" \
  -d '{"title":"Spoof body author","repo":"acme/app","author":"mallory@example.com"}')"
printf 'Body author=mallory@example.com -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]
BODY_AUTHOR_REFUSAL="$HTTP_BODY"

split_resp "$(curl -sS -w '\n%{http_code}' -X POST "${UI_URL}/api/tasks" \
  -H "Authorization: Bearer ${ALICE_TOK}" \
  -H "X-Forwarded-User: mallory@example.com" \
  -H "X-Grain-Client: spoof-check/1.0 session=xfu" \
  -H "Content-Type: application/json" \
  -d '{"title":"Task with forged X-Forwarded-User and body filedVia","repo":"acme/app","filedVia":"forged-body-via"}')"
[[ "$HTTP_STATUS" == "201" ]]
T_XFU="$(printf '%s' "$HTTP_BODY" | jq -r '.id')"
printf 'X-Forwarded-User=mallory + body filedVia=forged -> task %s authorLabel=%q provenance=%q filedVia=%q\n' \
  "$T_XFU" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.authorLabel')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.provenance')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.filedVia')" | tee -a "$OUT_TXT"

log_section "4. Reading back via grain get, grain list -filter, and grain fs read (grain task read)"

for tid in "$T_ALICE" "$T_BOB" "$T_CHILD" "$T_TASKBOT" "$T_XFU"; do
  {
    printf '\n--- grain get %s ---\n' "$tid"
    GRAIN_SERVER="${UI_URL}" GRAIN_TOKEN="${ACCESS_CODE}" "$GRAIN_BIN" get "$tid"
    printf '\n--- grain fs read task://%s/record.md (sandbox grain task read %s) ---\n' "$tid" "$tid"
    GRAIN_SERVER="${UI_URL}" GRAIN_TOKEN="${ACCESS_CODE}" "$GRAIN_BIN" fs read "task://${tid}/record.md"
  } >>"$OUT_TXT"
done

{
  printf '\n--- grain list -filter filed_via:has:sess-bob-202 ---\n'
  GRAIN_SERVER="${UI_URL}" GRAIN_TOKEN="${ACCESS_CODE}" "$GRAIN_BIN" list -filter "filed_via:has:sess-bob-202"
} | tee -a "$OUT_TXT"

log_section "5. SQLite rows and daemon journal lines"

SQLITE_TASKS="$(sqlite3 -json "${STORE_DB}" \
  "SELECT id, title, creator, origin_actor_kind, origin_actor_id, origin_behalf_kind, origin_behalf_id, origin_behalf_source, filed_via FROM task WHERE CAST(id AS INTEGER) >= ${T_ALICE} ORDER BY CAST(id AS INTEGER);")"
SQLITE_COMMENTS="$(sqlite3 -json "${STORE_DB}" \
  "SELECT id, task_id, body, author_kind, author_id, author_behalf_kind, author_behalf_id, author_behalf_source, filed_via FROM task_comment WHERE CAST(task_id AS INTEGER) >= ${T_ALICE} ORDER BY id;")"
SQLITE_TOKENS="$(sqlite3 -json "${STORE_DB}" \
  "SELECT id, name, prefix, owner, created_by, scope_set, scopes, repos FROM external_agent_token ORDER BY CAST(id AS INTEGER);")"

grep -E "grain api:" "${DAEMON_LOG}" | tee -a "$OUT_TXT"

API_ALICE="$(curl -fsS "${UI_URL}/api/tasks/${T_ALICE}" -H "X-Grain-Access-Code: ${ACCESS_CODE}")"
API_BOB="$(curl -fsS "${UI_URL}/api/tasks/${T_BOB}" -H "X-Grain-Access-Code: ${ACCESS_CODE}")"
API_CHILD="$(curl -fsS "${UI_URL}/api/tasks/${T_CHILD}" -H "X-Grain-Access-Code: ${ACCESS_CODE}")"
API_TASKBOT="$(curl -fsS "${UI_URL}/api/tasks/${T_TASKBOT}" -H "X-Grain-Access-Code: ${ACCESS_CODE}")"
API_XFU="$(curl -fsS "${UI_URL}/api/tasks/${T_XFU}" -H "X-Grain-Access-Code: ${ACCESS_CODE}")"

jq -n \
  --argjson aliceToken "$ALICE_VIEW" \
  --argjson bobToken "$BOB_VIEW" \
  --argjson childAgentToken "$CHILD_AGENT_VIEW" \
  --argjson taskMintedToken "$TASK_MINTED_VIEW" \
  --arg bodyAuthorRefusal "$BODY_AUTHOR_REFUSAL" \
  --argjson taskAlice "$API_ALICE" \
  --argjson taskBob "$API_BOB" \
  --argjson taskChild "$API_CHILD" \
  --argjson taskBot "$API_TASKBOT" \
  --argjson taskXfu "$API_XFU" \
  --argjson sqliteTasks "$SQLITE_TASKS" \
  --argjson sqliteComments "$SQLITE_COMMENTS" \
  --argjson sqliteTokens "$SQLITE_TOKENS" \
  '{
    tokens: {
      aliceSelf: $aliceToken,
      bobByOperator: $bobToken,
      aliceChildByExternalAgent: $childAgentToken,
      unownedByTask: $taskMintedToken
    },
    bodyAuthorRefusal: $bodyAuthorRefusal,
    apiTasks: {
      aliceSelf: $taskAlice,
      bobByOperator: $taskBob,
      aliceChildByExternalAgent: $taskChild,
      unownedByTask: $taskBot,
      xForwardedUserSpoof: $taskXfu
    },
    sqlite: {
      tasks: $sqliteTasks,
      comments: $sqliteComments,
      tokens: $sqliteTokens
    }
  }' >"$OUT_JSON"

echo "Item 2 completed cleanly: T_ALICE=${T_ALICE} T_BOB=${T_BOB} T_CHILD=${T_CHILD} T_TASKBOT=${T_TASKBOT} T_XFU=${T_XFU}"
