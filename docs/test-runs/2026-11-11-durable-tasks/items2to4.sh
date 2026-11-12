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

# §1 items 2-4 on task $1 (a durable task item1_compact.sh filed), with
# the fake agent resuming its session as a real CLI would (no no-session
# marker): grain compact with and without a handover, the same handover
# again, staleness by comments, and brief edits and refusals.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
T=$1
rm -f "$WORK_DIR/no-session"
run_once "$T" "$PARK" # a run that names a session, so later runs resume it

echo "== item 4: brief edits"
printf 'empty reason -> '; curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Authorization: Bearer $CODE" -d '{"brief":"x","reason":""}' "$DAEMON/api/tasks/$T/brief"
api POST "/api/tasks/$T/brief" '{"brief":"BRIEF-V1: maintain CHANGELOG.md; widget-999 is deprecated.","reason":"scope narrowed"}' |
	jq -c '{current_brief, original_description, edits:[.brief_edits[]|{by,reason}]}'
api PUT /api/settings '{"externalAgentApi":true}' >/dev/null
TOK=$(api POST /api/external-agents/tokens '{"name":"reader","scopes":["tasks:read","dispatch:read"],"repos":["acme/widgets"]}' | jq -r .token)
for p in brief compact rollover; do
	printf '%s with tasks:read+dispatch:read only -> ' "$p"
	curl -s -o /tmp/o.json -w '%{http_code} ' -X POST -H "Authorization: Bearer $TOK" -d '{"brief":"hijack","reason":"r"}' "$DAEMON/api/tasks/$T/$p"; jq -r .error /tmp/o.json
done
printf 'context with that token -> '; curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TOK" "$DAEMON/api/tasks/$T/context"

echo "== item 2a: grain compact with no handover sets a standing request"
"$G" compact "$T" | grep -i "requested"
run_once "$T" '{"kind":"mcp_sequence","calls":[{"tool":"compact_context","args":{"handover":"HANDOVER-V2: brief-driven; widget-999 deprecation noted."}},{"tool":"resume_later","args":{"reason":"compacted again","when":{"at":"1h"}}}]}'
f=$(latest_prompt "$T"); echo "$(basename "$f") resumed=$(grep -c 'Before the task below' "$f") brief=$(grep -c BRIEF-V1 "$f") original=$(grep -c ORIGINAL-DESCRIPTION "$f") request=$(grep -c 'asked for this task.s context to be compacted' "$f")"
api GET "/api/tasks/$T/context" | jq -c '{compacted_through,handover_version,compact_requested_at}'

echo "== item 2b: grain compact -handover-file, then the same file again"
echo "HANDOVER-V3: written by hand." >"$WORK_DIR/h3.txt"
"$G" compact -handover-file "$WORK_DIR/h3.txt" "$T" | grep -i "handover\|compacted"
api POST "/api/tasks/$T/comments" '{"body":"between the two compactions"}' >/dev/null
sleep 1
"$G" compact -handover-file "$WORK_DIR/h3.txt" "$T" >/dev/null
api GET "/api/tasks/$T/context" | jq -c '{compacted_through,handover_version,handover_confirmed_at,age:.handover_age}'
echo "handover comments: $(api GET "/api/tasks/$T" | jq -c '[.comments[]|select(.handover==true)|.body[0:14]]')"

echo "== item 3: staleness by comments (HandoverStaleComments = 30)"
for i in $(seq 29); do api POST "/api/tasks/$T/comments" "{\"body\":\"stale-drive $i\"}" >/dev/null; done
api GET "/api/tasks/$T/context" | jq -c '{age:.handover_age,stale:.handover_stale}'
api POST "/api/tasks/$T/comments" '{"body":"stale-drive 30"}' >/dev/null
api GET "/api/tasks/$T/context" | jq -c '{age:.handover_age,stale:.handover_stale}'
"$G" brief "$T" | grep "handover:"
run_once "$T" '{"kind":"mcp_sequence","calls":[{"tool":"write_handover","args":{"handover":"HANDOVER-V3: written by hand."}},{"tool":"resume_later","args":{"reason":"confirmed","when":{"at":"1h"}}}]}'
f=$(latest_prompt "$T"); sed 's/\\n/\n/g' "$f" | grep -o "\[grain\] This task's handover is stale[^.]*\." || echo "no stale note in $(basename "$f")"
api GET "/api/tasks/$T/context" | jq -c '{v:.handover_version,age:.handover_age,stale:.handover_stale}'
