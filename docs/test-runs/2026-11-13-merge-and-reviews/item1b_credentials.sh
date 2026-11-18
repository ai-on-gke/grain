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

# Item 1, second half, of docs/test-plans/2026-11-13.md §6:
#   - `grain connections import` round-trips merge_credential (store row,
#     API view, a second import of the same file changes nothing);
#   - with no merge credential, a merge goes out under the push
#     credential (alice-person) and the reviewed-merge gate trusting only
#     widgets-merger[bot] refuses it;
#   - what `grain health` says about reviewed-merge logins.
# Run after item1_2_merge_and_review.sh, whose merge it re-checks first.

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
RM=${REVIEWED_MERGE:-/home/debian/bin/reviewed-merge}
[[ -x $RM ]] || (cd "$ROOT" && go build -o "$RM" ./cmd/reviewed-merge)

echo "=== A. reviewed-merge on item 2's merge (merge credential)"
W=/tmp/walk-merge
sha=$(curl -s "http://$GHSIM/repos/acme/widgets/pulls/9000" | jq -r .merge_commit_sha)
set +e
GITHUB_API_URL="http://$GHSIM" "$RM" -repo acme/widgets -commit "$sha" -mergers 'widgets-merger[bot]' | tee "$OUT/item1-reviewed-merge-accept.json"
echo "exit=${PIPESTATUS[0]}"
set -e

echo "=== B. import round-trip"
sqlite3 "$W/data/store/grain.db" "select git_connections from grain_config" | tee "$OUT/item1-store-before.json"
"$GRAIN" connections -data-dir "$W/data" import "$W/seed-git.yaml"
sqlite3 "$W/data/store/grain.db" "select git_connections from grain_config" | tee "$OUT/item1-store-after.json"
cmp "$OUT/item1-store-before.json" "$OUT/item1-store-after.json" && echo "second import changed nothing"
CODE=$(tr -d '[:space:]' <"$W/data/local-api-token")
curl -s -H "Authorization: Bearer $CODE" "$DAEMON/api/deployment/git-connections" |
	jq -c '.[] | {credential,scope,mergeCredential}' | tee "$OUT/item1-api-view.jsonl"

echo "=== C. a merge with no merge credential"
stop_env /tmp/walk-merge
MERGE_CREDENTIAL="" boot /tmp/walk-merge-push \
	-upgrade-reviewed-merge-repo acme/widgets -upgrade-reviewed-merge-logins alice-person -upgrade-verify-cmd /bin/true
id=$(file_task "Add a note")
wait_for 180 '[[ $(task '"$id"' | jq -r .state) == "closed" ]]'
curl -s "http://$GHSIM/_sim/merges" | tee "$OUT/item1-push-merges.json"
echo
sha=$(curl -s "http://$GHSIM/repos/acme/widgets/pulls/9000" | jq -r .merge_commit_sha)
set +e
GITHUB_API_URL="http://$GHSIM" "$RM" -repo acme/widgets -commit "$sha" -mergers 'widgets-merger[bot]' 2>&1 | tee "$OUT/item1-reviewed-merge-refuse.txt"
echo "exit=${PIPESTATUS[0]}"
set -e
sqlite3 "$DB" "select kind, detail from task_event where task_id='$id' and kind='merged-as'"

echo "=== D. grain health (reviewed-merge logins: alice-person is a User account)"
GRAIN_SERVER="$DAEMON" GRAIN_TOKEN="$CODE" "$GRAIN" health 2>&1 | tee "$OUT/item1-health.txt" || true
api GET /api/sandboxes | jq -c '{mergeLoginWarnings}' | tee "$OUT/item1-health-api.json"
