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

# §1 item 5: roll over (by hand) a durable task holding an open pull
# request, a goal, a notification subscription, a dependent and a task
# parked on it; then the refusals; then the reconciler past the age limit
# with a run in flight. SQL=<cmd> runs SQL against the daemon's store
# (sqlite3 $DB, or psql $URL).
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
: "${SQL:?set SQL to a command that reads SQL on stdin}"
sql() { echo "$1" | $SQL; }
rm -f "$WORK_DIR/no-session"

new_task() { api POST /api/tasks "$1" | jq -r .id; }
P=$(new_task '{"title":"Durable predecessor","description":"PRED-DESCRIPTION","repo":"acme/widgets","admission":"durable","approve":true}')
api POST "/api/tasks/$P/approve" '{}' >/dev/null || true
cat >"$WORK_DIR/action-$P.json" <<JSON
{"kind":"mcp_sequence","calls":[
 {"tool":"run_command","args":{"command":"cd work && echo $P > walk.txt && git add walk.txt && git -c user.name=walk -c user.email=walk@example.com commit -qm 'chore: walk' && git push -q origin HEAD && grain pr open","description":"push and open PR"}},
 {"tool":"run_command","args":{"command":"grain handover write 'PRED-HANDOVER: PR open, waiting on review.'","description":"write the handover"}},
 {"tool":"resume_later","args":{"reason":"waiting on review","when":{"at":"1h"}}}]}
JSON
sleep 3; wait_rest "$P"
api POST "/api/tasks/$P/brief" '{"brief":"PRED-BRIEF: keep the PR moving.","reason":"walkthrough"}' >/dev/null
api PUT "/api/tasks/$P/goal" '{"kind":"person-closes"}' >/dev/null
sql "INSERT INTO task_notification_subscription (task_id, family) VALUES ('$P', 'durable.walk');"
D=$(new_task "{\"title\":\"Dependent of $P\",\"description\":\"waits\",\"repo\":\"acme/widgets\",\"approve\":true,\"dependsOn\":[\"$P\"]}")
api POST "/api/tasks/$D/depends-on" "{\"dependsOn\":\"$P\"}" >/dev/null 2>&1 || true
W=$(new_task '{"title":"Parks on the predecessor","description":"parks","repo":"acme/widgets","approve":true}')
echo "{\"kind\":\"mcp_sequence\",\"calls\":[{\"tool\":\"wait_for_task\",\"args\":{\"task_id\":\"$P\",\"park\":true}}]}" >"$WORK_DIR/action-$W.json"
api POST "/api/tasks/$W/approve" '{}' >/dev/null || true
# A wait_for_task park leaves W queued behind P rather than deferred.
for _ in $(seq 100); do [[ -s "$WORK_DIR/runs/mcp-$W.jsonl" ]] && break; sleep 0.3; done
sleep 3

echo "before: P=$P D=$D W=$W"
api GET "/api/tasks/$P" | jq -c '{state, pullRequest, goal:(.goal.kind // null)}'
api GET "/api/tasks/$D" | jq -c '{id,state,blocked}'
sql "SELECT run_id, resume_on FROM task_suspension WHERE task_id='$W' AND resumed_at IS NULL;"

echo "== grain rollover $P"
"$G" rollover "$P"
S=$(api GET "/api/tasks/$P" | jq -r '.rolledOverTo // empty')
echo "successor: $S"
api GET "/api/tasks/$P" | jq -c '{state, reason, rolledOverTo, pullRequest, goal:(.goal.kind // null)}'
api GET "/api/tasks/$S" | jq -c '{state, admission, rolledOverFrom, pullRequest, goal:(.goal.kind // null)}'
api GET "/api/tasks/$S" | jq -r .description | head -20
echo "W: $(api GET "/api/tasks/$W" | jq -c '{state,blocked}')"
api GET "/api/tasks/$D" | jq -c '{id,blocked}'
sql "SELECT run_id, resume_on FROM task_suspension WHERE task_id='$W' AND resumed_at IS NULL;"
for tbl in task_goal task_notification_subscription; do
	echo "$tbl rows for P=$(sql "SELECT COUNT(*) FROM $tbl WHERE task_id='$P';" | tr -d ' ' | grep -E '^[0-9]+$' | head -1) S=$(sql "SELECT COUNT(*) FROM $tbl WHERE task_id='$S';" | tr -d ' ' | grep -E '^[0-9]+$' | head -1)"
done
echo "== successor's first prompt"
run_once "$S" "$PARK"
f=$(latest_prompt "$S"); echo "$(basename "$f") brief=$(grep -c PRED-BRIEF "$f") handover=$(grep -c PRED-HANDOVER "$f") original=$(grep -c PRED-DESCRIPTION "$f")"

echo "== refusals"
printf 'again -> '; "$G" rollover "$P" 2>&1 | tail -1 || true
N=$(new_task '{"title":"Not durable","description":"plain","repo":"acme/widgets"}')
printf 'non-durable -> '; "$G" rollover "$N" 2>&1 | tail -1 || true

echo "== the reconciler, past the age limit, with a run in flight"
echo '{"kind":"mcp_sequence","hold":true,"hold_seconds":40,"calls":[{"tool":"resume_later","args":{"reason":"held","when":{"at":"1h"}}}]}' >"$WORK_DIR/action-$S.json"
api POST "/api/tasks/$S/run-now" '{}' >/dev/null
for _ in $(seq 150); do [[ -e "$WORK_DIR/live-$S.hold" ]] && break; sleep 0.2; done
sql "UPDATE task SET created_at = created_at - INTERVAL '400 days' WHERE id='$S';" 2>/dev/null ||
	sql "UPDATE task SET created_at = '2025-09-01 00:00:00 +0000 UTC' WHERE id='$S';"
sleep "${RECONCILE_WAIT:-20}"
echo "mid-run: $(api GET "/api/tasks/$S" | jq -c '{state, rolledOverTo}')"
rm -f "$WORK_DIR/live-$S.hold"
wait_rest "$S"
sleep "${RECONCILE_WAIT:-20}"
echo "after the run: $(api GET "/api/tasks/$S" | jq -c '{state, rolledOverTo}')"
