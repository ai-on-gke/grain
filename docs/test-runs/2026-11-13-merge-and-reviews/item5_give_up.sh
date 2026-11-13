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

# Item 5 of docs/test-plans/2026-11-13.md §6: when the merge queue gives up
# on a change, an Inbox item names the pull request and the reason
# (pipeline.merge-needs-person:<task>), and it clears once the block is
# lifted.
#
# The give-up is driven deterministically through the unfixable-check
# escalation: the fake agent seeds a failing `cla/google` check on its
# branch, and acme/widgets declares `cla/google` unfixable. The plan's
# shared-fault re-run (a red GitHub Actions job grain attributed to a
# shared fault) needs Actions run logs the simulator does not serve, so it
# is left to TestASharedFaultOnlyRedIsReRunOnceTheBaseGoesGreen.
#
# KEEP=1 leaves the daemon running for ui_inbox.mjs.

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
boot /tmp/walk-merge
api PUT /api/repos/acme/widgets/unfixable-checks '{"unfixableChecks":["cla/google"]}' | jq -c .
cat >"$WORK_DIR/action-default.json" <<J
{"kind":"mcp_sequence","calls":[
 {"tool":"run_command","args":{"command":"cd work && br=\$(git rev-parse --abbrev-ref HEAD) && curl -s -X POST http://$GHSIM/_sim/check-runs -d '{\"ref\":\"'\$br'\",\"checks\":[{\"Name\":\"cla/google\",\"Status\":\"completed\",\"Conclusion\":\"failure\"},{\"Name\":\"unit\",\"Status\":\"completed\",\"Conclusion\":\"success\"}]}' && mkdir -p notes && echo 'change from task {TASK}' > notes/{TASK}.txt && git add -A && git commit -qm 'docs: note from task {TASK}' -m 'Walkthrough change.' && git push -q origin HEAD","description":"pushing a change whose cla/google check is red"}},
 {"tool":"comment_on_issue","args":{"comment":"pushed"}}
]}
J
id=$(file_task "Add a note the queue gives up on")
wait_for 240 '[[ $(api GET "/api/notifications?open=true&family=pipeline.merge-needs-person" | jq "length") -ge 1 ]]' || true
task "$id" | jq -c '{state,mergeQueueState,mergeQueueNote: .mergeQueueNote[0:300]}' | tee "$OUT/item5-change.json"
api GET "/api/notifications?open=true&family=pipeline.merge-needs-person" | jq -c '.[] | {subject,title,taskId,repo,actions}' | tee "$OUT/item5-notification.jsonl"
api GET "/api/notifications?open=true&family=pipeline.merge-needs-person" | jq -r '.[0].body' | tee "$OUT/item5-notification-body.md"
api GET /api/inbox | jq -c '.[]? // .items[]? | {kind,taskId,title: (.title // .subject)}' 2>/dev/null | tee "$OUT/item5-inbox.jsonl"
GRAIN_SERVER="$DAEMON" GRAIN_TOKEN="$CODE" "$GRAIN" inbox 2>&1 | tee "$OUT/item5-cli-inbox.txt" || true
if [[ ${KEEP:-} == 1 ]]; then echo "daemon left running at $DAEMON (task $id)"; exit 0; fi
echo "=== Retry merge clears the item"
api POST "/api/tasks/$id/retry-merge" | jq -c '{state}' || true
sleep 10
api GET "/api/notifications?open=true&family=pipeline.merge-needs-person" | jq -c 'length' | tee "$OUT/item5-after-retry.txt"
