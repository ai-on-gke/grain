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

# §1 item 1: a durable task, ~30 comments over three parked runs, then a
# run that calls compact_context while a person comments mid-run, then one
# more run whose prompt is the post-compaction one.
# Usage: item1_compact.sh   (prints the new task's id last)
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

T=$(api POST /api/tasks '{"title":"Maintain the widgets changelog","description":"ORIGINAL-DESCRIPTION: keep CHANGELOG.md in acme/widgets current.","repo":"acme/widgets","admission":"durable","approve":true}' | jq -r .id)
api POST "/api/tasks/$T/approve" '{}' >/dev/null || true
echo "$PARK" >"$WORK_DIR/action-$T.json"
sleep 3; wait_rest "$T"
for round in 1 2 3; do
	for i in $(seq 10); do
		api POST "/api/tasks/$T/comments" "{\"body\":\"round $round note $i: changelog entry for widget-$round$i is settled; ship it in the next release with the usual wording and nothing more.\"}" >/dev/null
	done
	run_once "$T" "$PARK"
done
before=$(latest_prompt "$T")

cat >"$WORK_DIR/action-$T.json" <<JSON
{"kind":"mcp_sequence","hold":true,"hold_seconds":60,"calls":[
 {"tool":"compact_context","args":{"handover":"HANDOVER-V-COMPACT: rounds 1-3 settled widget-11..widget-310 entries; nothing open."}},
 {"tool":"resume_later","args":{"reason":"compacted","when":{"at":"1h"}}}]}
JSON
api POST "/api/tasks/$T/run-now" '{}' >/dev/null
for _ in $(seq 150); do [[ -e "$WORK_DIR/live-$T.hold" ]] && break; sleep 0.2; done
sleep 1
mid=$(api POST "/api/tasks/$T/comments" '{"body":"MIDRUN-COMMENT: also mention the widget-999 deprecation."}' | jq -r '.id // .comment.id // empty')
rm -f "$WORK_DIR/live-$T.hold"
wait_rest "$T"
run_once "$T" "$PARK"
after=$(latest_prompt "$T")

ctx=$(api GET "/api/tasks/$T/context")
echo "context: $(echo "$ctx" | jq -c '{compacted_through,handover_version,compacted_by,summarised}')"
echo "comments around the cursor:"
api GET "/api/tasks/$T" | jq -c '.comments[] | {id, a:.author, b:.body[0:40]}' | tail -8
for f in "$before" "$after"; do
	echo "$(basename "$f"): bytes=$(wc -c <"$f") round-notes=$(grep -o 'round [0-9] note' "$f" | wc -l) midrun=$(grep -c MIDRUN-COMMENT "$f") handover=$(grep -c HANDOVER-V-COMPACT "$f") note=$(grep -c 'summarised by the handover below' "$f")"
done
echo "TASK=$T"
