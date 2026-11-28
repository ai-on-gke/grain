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

# item2-earlier-head.sh: §11 item 2. A pull request carol approved on its
# first head, then bob pushed a second commit and merged it: the approval
# is of an earlier head, so the merge is held. Also: a review by a
# non-member (CONTRIBUTOR) on the final head and the author's own
# approval do not count. Then an agent credential holding
# settings:write and capabilities:admin (the documented agent bar)
# accepts it.
here=$(dirname "$0")
. "$here/lib.sh"
git -C "$W/clone" checkout -q -b pr6 main
echo "first head" >>"$W/clone/README.md"
git -C "$W/clone" commit -qam "pr6: first head"
early=$(git -C "$W/clone" rev-parse HEAD)
echo "second head, after the approval" >>"$W/clone/README.md"
git -C "$W/clone" commit -qam "pr6: second head"
final=$(git -C "$W/clone" rev-parse HEAD)
git -C "$W/clone" checkout -q main
git -C "$W/clone" merge -q --no-ff -m "Merge pull request from pr6" pr6
m6=$(git -C "$W/clone" rev-parse HEAD)
add_pull 6 "$m6" "$final" bob bob '[
 {"user":"carol","state":"APPROVED","commit_id":"'"$early"'","author_association":"MEMBER"},
 {"user":"bob","state":"APPROVED","commit_id":"'"$final"'","author_association":"OWNER"},
 {"user":"mallory","state":"APPROVED","commit_id":"'"$final"'","author_association":"CONTRIBUTOR"}]'
push
echo "m6=$m6" | tee -a "$W/item1.shas"
wait_sync
echo "after push: $(state)"
agent=$(api POST /api/external-agents/tokens '{"name":"walk-admin-agent","scopes":["settings:read","settings:write","capabilities:admin"]}' | jq -r .token)
weak=$(api POST /api/external-agents/tokens '{"name":"walk-settings-agent","scopes":["settings:read","settings:write"]}' | jq -r .token)
body=$(jq -nc --arg r "$REMOTE" --arg s "$m6" '{remote:$r, branch:"main", acceptCommit:$s}')
for who in weak agent; do
	tok=${!who}
	out=$(curl -s -w "\n%{http_code}" -X POST -H "Authorization: Bearer $tok" -H "X-Grain-Acting-Reason: accept the merge of pr6 after reading its diff" "$API/api/state-repo/remote" -d "$body")
	echo "$who: HTTP $(tail -1 <<<"$out") $(head -n -1 <<<"$out" | jq -c '.error // "accepted"' | cut -c1-260)"
done
wait_sync
echo "after accept: $(state)"
