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

# item1-accept.sh: who may Accept the commit item1-sequence.sh left held.
# setup.sh maps this uid to carol@example.com on the API socket and puts
# carol on grain/viewer (a limited role). Tries, in order: an external agent credential holding
# settings:write, carol over the socket, then the access code (an
# unlimited role) -- and prints each answer and what is held after it.
here=$(dirname "$0")
. "$here/lib.sh"
. "$W/item1.shas"
body=$(jq -nc --arg r "$REMOTE" --arg s "$u4" '{remote:$r, branch:"main", acceptCommit:$s}')
call() { # call LABEL CURL-ARGS...
	local label=$1
	shift
	local out
	out=$(curl -s -w '\n%{http_code}' -X POST "$@" -d "$body")
	echo "$label: HTTP $(tail -1 <<<"$out") $(head -n -1 <<<"$out" | jq -c '.error // .verification.held.sha // "accepted"' | cut -c1-260)"
}
agent=$(api POST /api/external-agents/tokens '{"name":"walk-settings-writer","scopes":["settings:read","settings:write"]}' | jq -r .token)
call "agent (settings:write)" -H "Authorization: Bearer $agent" "$API/api/state-repo/remote"
call "carol (grain/viewer, socket)" --unix-socket "$W/data/api/grain.sock" http://localhost/api/state-repo/remote
echo "still held: $(state)"
call "access code (unlimited)" -H "Authorization: Bearer $TOK" "$API/api/state-repo/remote"
wait_sync
echo "after accept: $(state)"
echo "m5=$m5"
tail -3 "$W/data/state-repo/README.md"
