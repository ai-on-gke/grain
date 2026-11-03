#!/usr/bin/env bash
# The live part of item 3 (sticky auto_merge: false) against the demo
# matrix.sh left running (REUSE=1): an agent files a proposal the way
# propose_task's auto_merge: false does (declineAutoMerge), the agent and
# an external token try to turn it on, a person approves it, and then
# alice Submits through the simulated IAP. The hold, release, carried
# clearance and restart need dispatch and a review run, which grain demo
# does not have: TestDeclinedAutoMergeOutlastsHoldReleaseCarryAndRestartUntilSubmit
# (pkg/orchestrator) walks those.
#
#   sticky.sh [OUT]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/env.sh"
OUT=${1:-/tmp/oa}
CODE=$(cat "$OUT/access-code")
DB=$(ls "$OUT"/tmp/grain-demo-*/grain.db | head -1)
AGENT=$(sqlite3 "$DB" "select id from task where title='agent host task'")
ag() { curl -s -H "Authorization: Bearer $CODE" -H "X-Grain-Acting-Task: $AGENT" -H 'X-Grain-Acting-Reason: walkthrough' -H 'Content-Type: application/json' "$@"; }

ID=$(ag -X POST "$DEMO/api/tasks" -d '{"title":"proposed with auto_merge false","repo":"acme/widgets","autoMerge":false,"declineAutoMerge":true}' | jq -r .id)
echo "proposal $ID"
echo "agent PATCH autoMerge true: $(ag -o /dev/null -w '%{http_code}' -X PATCH "$DEMO/api/tasks/$ID" -d '{"autoMerge":true}')"
echo "access code approves: $(code -o /dev/null -w '%{http_code}' -X POST "$DEMO/api/tasks/$ID/approve" -d '{}')"
echo "after approval: $(code "$DEMO/api/tasks/$ID" | jq -c '{autoMerge}')"
echo "alice Submits: $(iap -o /dev/null -w '%{http_code}' -X POST "$PROXY/api/tasks/$ID/submit" -d '{}')"
echo "after Submit: $(code "$DEMO/api/tasks/$ID" | jq -c '{autoMerge}')"
sqlite3 -json "$DB" "select id, kind, actor_kind, actor_id, actor_source, reason, detail from task_event where task_id='$ID' order by id" > "$OUT/dump-sticky-task_event.json"
sqlite3 -json "$DB" "select task_id, kind, target from task_link where task_id='$ID'" > "$OUT/dump-sticky-task_link.json"
jq -c '.[]' "$OUT/dump-sticky-task_event.json" "$OUT/dump-sticky-task_link.json"
