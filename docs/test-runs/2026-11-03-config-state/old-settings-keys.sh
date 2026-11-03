#!/bin/sh
# Item 7, the "old keys" half, against the walkthrough daemon (internal
# grain/config) at 127.0.0.1:18702:
#  a. PUT /api/settings with the fields #443 removed (mergeBatchSize,
#     mergeGatherWindow);
#  b. `grain state check` on a copy of the export whose grain_config row
#     carries merge_batch_size changed, and one carrying a key no build has
#     (merge_batching_enabled), the way an old or hand-edited config repo would.
set -u
G=/home/debian/bin/grain
T=$(cat /home/debian/live/.grain/local-api-token)
echo "== a. PUT /api/settings with retired fields"
curl -s -X PUT -H "Authorization: Bearer $T" -H 'Content-Type: application/json' \
  http://127.0.0.1:18702/api/settings -d '{"mergeBatchSize":5,"mergeGatherWindow":"2m"}' -w '\nHTTP %{http_code}\n' | tail -c 400
curl -s -H "Authorization: Bearer $T" http://127.0.0.1:18702/api/settings | jq -c 'to_entries | map(select(.key|test("(?i)batch|gather|recheck"))) | from_entries'
W=$(mktemp -d)
git clone -q /home/debian/live/.grain/state-repo "$W/a"
jq '.[0].merge_batch_size = 9' "$W/a/tables/grain_config.json" > "$W/x" && mv "$W/x" "$W/a/tables/grain_config.json"
echo "== b1. state check, merge_batch_size = 9"; $G state check "$W/a"; echo "exit=$?"
git clone -q /home/debian/live/.grain/state-repo "$W/b"
jq '.[0].merge_batching_enabled = true' "$W/b/tables/grain_config.json" > "$W/x" && mv "$W/x" "$W/b/tables/grain_config.json"
echo "== b2. state check, unknown key merge_batching_enabled"; $G state check "$W/b"; echo "exit=$?"
