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

# Item 3 walkthrough: Owner trust auto-approval, repo boundaries, priority /
# keep_running / requires_person holds, PATCH withdrawal, and token rotation.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$DIR/env.sh" start
# shellcheck source=/dev/null
source "${WALKTHROUGH_DIR:-/var/tmp/grain-walkthrough-3070}/vars.env"

OUT_TXT="$DIR/item3-owner-trust.txt"
OUT_JSON="$DIR/item3-owner-trust.json"
: >"$OUT_TXT"

log_section() {
  printf '\n=== %s ===\n' "$1" | tee -a "$OUT_TXT"
}

iap_req() {
  local method="$1" email="$2" path="$3" body="${4:-}"
  if [[ -n "$body" ]]; then
    curl -sS -w '\n%{http_code}' -X "$method" "${IAP_URL}${path}" \
      -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
      -H "X-Iapsim-Email: ${email}" \
      -H "Content-Type: application/json" \
      -d "$body"
  else
    curl -sS -w '\n%{http_code}' -X "$method" "${IAP_URL}${path}" \
      -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
      -H "X-Iapsim-Email: ${email}"
  fi
}

ext_req() {
  local method="$1" tok="$2" path="$3" body="${4:-}" extra_hdr="${5:-}"
  local args=(-sS -w '\n%{http_code}' -X "$method" "${UI_URL}${path}"
    -H "Authorization: Bearer ${tok}"
    -H "X-Grain-Acting-Reason: walkthrough item 3"
    -H "Content-Type: application/json")
  if [[ -n "$extra_hdr" ]]; then
    args+=(-H "$extra_hdr")
  fi
  if [[ -n "$body" ]]; then
    args+=(-d "$body")
  fi
  curl "${args[@]}"
}

split_resp() {
  HTTP_STATUS="$(printf '%s' "$1" | tail -n1)"
  HTTP_BODY="$(printf '%s' "$1" | sed '$d')"
}

log_section "1. Issue credentials for alice and bob; configure ownerTrust=(alice@example.com, acme/app)"

split_resp "$(iap_req POST "operator@example.com" "/api/external-agents/tokens" \
  '{"name":"alice-dev","scopeSet":"grain/external-dev","owner":"alice@example.com"}')"
[[ "$HTTP_STATUS" == "201" ]]
ALICE_ID="$(printf '%s' "$HTTP_BODY" | jq -r '.id')"
ALICE_TOK="$(printf '%s' "$HTTP_BODY" | jq -r '.token')"

split_resp "$(iap_req POST "operator@example.com" "/api/external-agents/tokens" \
  '{"name":"bob-dev","scopeSet":"grain/external-dev","owner":"bob@example.com"}')"
[[ "$HTTP_STATUS" == "201" ]]
BOB_TOK="$(printf '%s' "$HTTP_BODY" | jq -r '.token')"

# External-dev cannot write ownerTrust settings itself -> 403
split_resp "$(ext_req PUT "$ALICE_TOK" "/api/settings" \
  '{"ownerTrust":[{"owner":"alice@example.com","repo":"*"}]}')"
printf 'external-dev PUT /api/settings ownerTrust -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]

# Operator configures ownerTrust for (alice@example.com, acme/app)
split_resp "$(iap_req PUT "operator@example.com" "/api/settings" \
  '{"ownerTrust":[{"owner":"alice@example.com","repo":"acme/app"}]}')"
[[ "$HTTP_STATUS" == "200" ]]
printf 'Configured ownerTrust: %s\n' "$(printf '%s' "$HTTP_BODY" | jq -c '.ownerTrust')" | tee -a "$OUT_TXT"

# Configure capability-policy marking in-process capability read-issues as requires_person: true
split_resp "$(iap_req PUT "operator@example.com" "/api/capability-policy" \
  '{"policy":{"catalogue":{"inProcess":{"read-issues":{"restrictions":{"requiresPerson":true}}}}}}')"
printf 'PUT /api/capability-policy -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "200" ]]

log_section "2. Trusted (owner, repo) vs untrusted repo, cross-repo reads/writes, and spoofed owner"

# 2a. Alice on acme/app -> queued by owner-trust for alice@example.com
split_resp "$(ext_req POST "$ALICE_TOK" "/api/tasks" '{"title":"Trusted filing on acme/app","repo":"acme/app"}')"
[[ "$HTTP_STATUS" == "201" ]]
T_TRUSTED_JSON="$HTTP_BODY"
T_TRUSTED_ID="$(printf '%s' "$HTTP_BODY" | jq -r '.id')"
printf 'Alice on acme/app -> id=%s state=%s approvedBy=%s approvedByLabel=%q\n' \
  "$T_TRUSTED_ID" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.approvedBy')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.approvedByLabel')" | tee -a "$OUT_TXT"
[[ "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" == "queued" ]]
[[ "$(printf '%s' "$HTTP_BODY" | jq -r '.approvedBy')" == "owner-trust" ]]

# 2b. Alice on acme/other -> stays proposed
split_resp "$(ext_req POST "$ALICE_TOK" "/api/tasks" '{"title":"Untrusted repo acme/other","repo":"acme/other"}')"
[[ "$HTTP_STATUS" == "201" ]]
T_OTHER_JSON="$HTTP_BODY"
printf 'Alice on acme/other -> state=%s approvedBy=%s\n' \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.approvedBy // "none"')" | tee -a "$OUT_TXT"
[[ "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" == "proposed" ]]

# 2c. Alice on acme/app with cross-repo reads: ["acme/other"] -> stays proposed
split_resp "$(ext_req POST "$ALICE_TOK" "/api/tasks" '{"title":"Cross-repo read on acme/other","repo":"acme/app","reads":["acme/other"]}')"
[[ "$HTTP_STATUS" == "201" ]]
printf 'Alice on acme/app + reads=[acme/other] -> state=%s\n' "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" | tee -a "$OUT_TXT"
[[ "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" == "proposed" ]]

# 2d. Bob on acme/app (with spoofed X-Forwarded-User: alice@example.com) -> stays proposed
split_resp "$(ext_req POST "$BOB_TOK" "/api/tasks" '{"title":"Bob on acme/app spoofing alice","repo":"acme/app"}' "X-Forwarded-User: alice@example.com")"
[[ "$HTTP_STATUS" == "201" ]]
T_BOB_JSON="$HTTP_BODY"
printf 'Bob on acme/app (X-Forwarded-User=alice) -> state=%s authorLabel=%q\n' \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.authorLabel')" | tee -a "$OUT_TXT"
[[ "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" == "proposed" ]]

log_section "3. Priority, keep_running, and requires_person grants hold or refuse for a person"

# 3a. Standard grain/external-dev escalation rails (403 Forbidden)
split_resp "$(ext_req POST "$ALICE_TOK" "/api/tasks" '{"title":"Raised priority from external-dev","repo":"acme/app","priority":6}')"
printf 'external-dev priority=6 -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]
REFUSAL_PRIO="$HTTP_BODY"

split_resp "$(ext_req POST "$ALICE_TOK" "/api/tasks" '{"title":"keepRunning from external-dev","repo":"acme/app","keepRunning":true}')"
printf 'external-dev keepRunning=true -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]
REFUSAL_KEEP_RUNNING="$HTTP_BODY"

split_resp "$(ext_req POST "$ALICE_TOK" "/api/tasks" '{"title":"requires_person capability without cap admin","repo":"acme/app","capabilities":["read-issues"]}')"
printf 'external-dev capabilities=[read-issues] -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]
REFUSAL_CAP_NO_ADMIN="$HTTP_BODY"

# 3b. Even when Alice's external token holds tasks:admin + capabilities:admin,
#     owner-trust STILL holds priority > 5, keepRunning, and requires_person
#     grants in 'proposed' for a person!
split_resp "$(iap_req POST "operator@example.com" "/api/external-agents/tokens" \
  '{"name":"alice-admin-ext","scopes":["tasks:read","tasks:write","tasks:admin","capabilities:admin"],"owner":"alice@example.com"}')"
[[ "$HTTP_STATUS" == "201" ]]
ALICE_ADMIN_TOK="$(printf '%s' "$HTTP_BODY" | jq -r '.token')"

# Clean baseline with alice-admin-ext -> queued by owner-trust
split_resp "$(ext_req POST "$ALICE_ADMIN_TOK" "/api/tasks" '{"title":"Admin ext clean baseline","repo":"acme/app"}')"
[[ "$HTTP_STATUS" == "201" ]]
[[ "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" == "queued" ]]
printf 'alice-admin-ext baseline -> state=%s approvedBy=%s\n' \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.approvedBy')" | tee -a "$OUT_TXT"

# Raised priority=8 -> held in proposed (not auto-approved by owner-trust)
split_resp "$(ext_req POST "$ALICE_ADMIN_TOK" "/api/tasks" '{"title":"Admin ext priority 8","repo":"acme/app","priority":8,"approved":false}')"
printf 'alice-admin-ext priority=8 -> HTTP %s: %s\n' "$HTTP_STATUS" "$(printf '%s' "$HTTP_BODY" | jq -c '{id,state,priority,approvedBy}')" | tee -a "$OUT_TXT"
HOLD_PRIO_JSON="$HTTP_BODY"
if [[ "$HTTP_STATUS" == "201" ]]; then
  [[ "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" == "proposed" ]]
else
  [[ "$HTTP_STATUS" == "403" ]]
fi

# keepRunning=true -> held in proposed (or refused 403)
split_resp "$(ext_req POST "$ALICE_ADMIN_TOK" "/api/tasks" '{"title":"Admin ext keepRunning","repo":"acme/app","keepRunning":true,"approved":false}')"
printf 'alice-admin-ext keepRunning=true -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
HOLD_KEEP_RUNNING_JSON="$HTTP_BODY"
if [[ "$HTTP_STATUS" == "201" ]]; then
  [[ "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" == "proposed" ]]
else
  [[ "$HTTP_STATUS" == "403" ]]
fi

# requires_person capability (read-issues) -> refused 403 for external agents
split_resp "$(ext_req POST "$ALICE_ADMIN_TOK" "/api/tasks" '{"title":"Admin ext requires_person grant","repo":"acme/app","capabilities":["read-issues"],"approved":false}')"
printf 'alice-admin-ext capabilities=[read-issues] -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
HOLD_REQ_PERSON_JSON="$HTTP_BODY"
[[ "$HTTP_STATUS" == "403" ]]

log_section "4. PATCH withdrawal on repo move or cross-owner edit"

# In-scope title edit by Alice keeps owner-trust approval
split_resp "$(ext_req PATCH "$ALICE_TOK" "/api/tasks/${T_TRUSTED_ID}" '{"title":"Trusted filing edited in acme/app"}')"
[[ "$HTTP_STATUS" == "200" ]]
[[ "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" == "queued" ]]
printf 'Alice PATCH title in acme/app -> state=%s approvedBy=%s\n' \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.approvedBy')" | tee -a "$OUT_TXT"

# Moving repo to acme/other withdraws owner-trust approval -> proposed
split_resp "$(ext_req PATCH "$ALICE_TOK" "/api/tasks/${T_TRUSTED_ID}" '{"repo":"acme/other"}')"
[[ "$HTTP_STATUS" == "200" ]]
PATCH_MOVED_JSON="$HTTP_BODY"
printf 'Alice PATCH repo=acme/other -> state=%s approvedBy=%s\n' \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.approvedBy // "none"')" | tee -a "$OUT_TXT"
[[ "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" == "proposed" ]]

log_section "5. Token rotation preserves owner; agents cannot rotate another owner's token or widen scopes"

# Bob's capabilities:admin agent tries to rotate Alice's token -> 403
split_resp "$(iap_req POST "operator@example.com" "/api/external-agents/tokens" \
  '{"name":"bob-cap-admin","scopes":["capabilities:admin","tasks:read","tasks:write","ci:read","ci:write","merge:write","repos:read","sandboxes:read","templates:read","deployment:read"],"owner":"bob@example.com"}')"
[[ "$HTTP_STATUS" == "201" ]]
BOB_CAP_ADMIN_TOK="$(printf '%s' "$HTTP_BODY" | jq -r '.token')"

split_resp "$(ext_req POST "$BOB_CAP_ADMIN_TOK" "/api/external-agents/tokens/${ALICE_ID}/rotate" '{}')"
printf 'Bob cap-admin agent rotating Alice token -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]
BOB_ROTATE_REFUSAL="$HTTP_BODY"

# Acting task holding api:capabilities:admin + api:grain/external-dev tries to rotate Alice's token -> 403
TASK_ROT_JSON="$(curl -fsS -X POST "${UI_URL}/api/tasks" \
  -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
  -H "Content-Type: application/json" \
  -d '{"title":"Task attempting token rotation","repo":"acme/app","approved":false,"capabilities":["api:capabilities:admin","api:tasks:read","api:tasks:write"]}')"
TASK_ROT_ID="$(printf '%s' "$TASK_ROT_JSON" | jq -r '.id')"

split_resp "$(curl -sS -w '\n%{http_code}' -X POST "${UI_URL}/api/external-agents/tokens/${ALICE_ID}/rotate" \
  -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
  -H "X-Grain-Acting-Task: ${TASK_ROT_ID}" \
  -H "X-Grain-Acting-Reason: walkthrough item 3 task rotate attempt" \
  -H "Content-Type: application/json" \
  -d '{}')"
printf 'Acting task rotating Alice token -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "403" ]]
TASK_ROTATE_REFUSAL="$HTTP_BODY"

# Alice rotates her own token via IAP -> 200 OK, owner=alice@example.com preserved
OLD_ALICE_TOK="$ALICE_TOK"
split_resp "$(iap_req POST "alice@example.com" "/api/external-agents/tokens/${ALICE_ID}/rotate" '{}')"
[[ "$HTTP_STATUS" == "200" ]]
NEW_ALICE_TOK="$(printf '%s' "$HTTP_BODY" | jq -r '.token')"
ROTATED_VIEW="$(printf '%s' "$HTTP_BODY" | jq 'del(.token)')"
printf 'Rotated Alice token view: %s\n' "$(printf '%s' "$ROTATED_VIEW" | jq -c .)" | tee -a "$OUT_TXT"
[[ "$(printf '%s' "$ROTATED_VIEW" | jq -r '.owner')" == "alice@example.com" ]]

# Old token is now rejected 401
split_resp "$(ext_req GET "$OLD_ALICE_TOK" "/api/tasks")"
printf 'Old rotated-out token GET /api/tasks -> HTTP %s: %s\n' "$HTTP_STATUS" "$HTTP_BODY" | tee -a "$OUT_TXT"
[[ "$HTTP_STATUS" == "401" ]]

# Rotated token still files on acme/app and lands queued via owner-trust!
split_resp "$(ext_req POST "$NEW_ALICE_TOK" "/api/tasks" '{"title":"Post-rotation trusted filing on acme/app","repo":"acme/app"}')"
[[ "$HTTP_STATUS" == "201" ]]
POST_ROT_TASK="$HTTP_BODY"
printf 'Post-rotation Alice filing on acme/app -> state=%s approvedBy=%s approvedByLabel=%q\n' \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.approvedBy')" \
  "$(printf '%s' "$HTTP_BODY" | jq -r '.approvedByLabel')" | tee -a "$OUT_TXT"
[[ "$(printf '%s' "$HTTP_BODY" | jq -r '.state')" == "queued" ]]

SQLITE_APPROVALS="$(sqlite3 -json "${STORE_DB}" \
  "SELECT id, title, target_owner || '/' || target_name AS repo, priority, keep_running, creator, approval_actor_kind, approval_actor_id, approval_behalf_kind, approval_behalf_id FROM task WHERE CAST(id AS INTEGER) >= ${T_TRUSTED_ID} ORDER BY CAST(id AS INTEGER);")"

jq -n \
  --argjson trustedTask "$T_TRUSTED_JSON" \
  --argjson untrustedRepoTask "$T_OTHER_JSON" \
  --argjson bobSpoofTask "$T_BOB_JSON" \
  --arg refusalPriority "$REFUSAL_PRIO" \
  --arg refusalKeepRunning "$REFUSAL_KEEP_RUNNING" \
  --arg refusalCapNoAdmin "$REFUSAL_CAP_NO_ADMIN" \
  --arg holdPrio "$HOLD_PRIO_JSON" \
  --arg holdKeepRunning "$HOLD_KEEP_RUNNING_JSON" \
  --arg holdReqPerson "$HOLD_REQ_PERSON_JSON" \
  --argjson patchMovedTask "$PATCH_MOVED_JSON" \
  --arg bobRotateRefusal "$BOB_ROTATE_REFUSAL" \
  --arg taskRotateRefusal "$TASK_ROTATE_REFUSAL" \
  --argjson rotatedToken "$ROTATED_VIEW" \
  --argjson postRotationTask "$POST_ROT_TASK" \
  --argjson sqliteApprovals "$SQLITE_APPROVALS" \
  '{
    trustedTask: $trustedTask,
    untrustedRepoTask: $untrustedRepoTask,
    bobSpoofTask: $bobSpoofTask,
    externalDevRefusals: {
      priority6: $refusalPriority,
      keepRunning: $refusalKeepRunning,
      capabilityWithoutAdmin: $refusalCapNoAdmin
    },
    adminExtHolds: {
      priority8: ($holdPrio | try fromjson catch $holdPrio),
      keepRunning: ($holdKeepRunning | try fromjson catch $holdKeepRunning),
      requiresPersonGrant: ($holdReqPerson | try fromjson catch $holdReqPerson)
    },
    patchMovedTask: $patchMovedTask,
    rotationGuards: {
      bobCapAdminRotateRefusal: $bobRotateRefusal,
      actingTaskRotateRefusal: $taskRotateRefusal,
      rotatedToken: $rotatedToken,
      postRotationTask: $postRotationTask
    },
    sqliteApprovals: $sqliteApprovals
  }' >"$OUT_JSON"

echo "Item 3 completed cleanly!"
