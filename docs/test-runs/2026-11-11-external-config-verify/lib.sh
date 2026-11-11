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

# lib.sh: helpers the §11 walkthrough sources. W is the scratch root
# (forge/, data/, clone/), forge.go serves $W/forge on 127.0.0.1:4291 and
# the daemon listens on 127.0.0.1:4292.
set -euo pipefail
W=${W:-/home/debian/w11}
API=${API:-http://127.0.0.1:4292}
PULLS=$W/forge/pulls.json
TOK=$(cat "$W/data/local-api-token")

api() { # api METHOD PATH [JSON]
	curl -s -H "Authorization: Bearer $TOK" -X "$1" "$API$2" ${3:+-d "$3"}
}

# set_remote JSON: POST /api/state-repo/remote with the remote and branch
# restated (the route refuses a request without a URL) and JSON's fields.
REMOTE=http://127.0.0.1:4291/acme/grain-config.git
set_remote() {
	api POST /api/state-repo/remote "$(jq -nc --arg r "$REMOTE" --argjson x "$1" '{remote:$r, branch:"main"} + $x')"
}

state() { api GET /api/state-repo | jq -c '{head, held: .verification.held, forgeReviews: .verification.forgeReviews}'; }

# add_pull NUMBER MERGE_SHA HEAD_SHA AUTHOR MERGED_BY REVIEWS_JSON
add_pull() {
	jq --argjson n "$1" --arg m "$2" --arg h "$3" --arg a "$4" --arg b "$5" --argjson r "$6" \
		'. + [{number:$n, base:"main", merge_commit_sha:$m, head_sha:$h, author:$a, merged_by:$b, reviews:$r}]' \
		"$PULLS" >"$PULLS.tmp" && mv "$PULLS.tmp" "$PULLS"
}

# pr_merge BRANCH FILE TEXT: a feature branch with one commit, merged
# --no-ff into main; prints "<merge sha> <head sha>".
pr_merge() {
	git -C "$W/clone" checkout -q -b "$1" main
	echo "$3" >>"$W/clone/$2"
	git -C "$W/clone" commit -qam "$1: $3"
	local head
	head=$(git -C "$W/clone" rev-parse HEAD)
	git -C "$W/clone" checkout -q main
	git -C "$W/clone" merge -q --no-ff -m "Merge pull request from $1" "$1"
	echo "$(git -C "$W/clone" rev-parse HEAD) $head"
}

# direct FILE TEXT [sign]: a commit straight on main, optionally SSH-signed.
direct() {
	echo "$2" >>"$W/clone/$1"
	if [ "${3:-}" = sign ]; then
		git -C "$W/clone" -c gpg.format=ssh -c user.signingkey="$W/signing_key.pub" commit -qS -am "direct: $2"
	else
		git -C "$W/clone" commit -qam "direct: $2"
	fi
	git -C "$W/clone" rev-parse HEAD
}

push() { git -C "$W/clone" push -q "$@" origin main; }

# wait_sync: one daemon sync tick (10s reconcile) and a little more.
wait_sync() { sleep "${1:-13}"; }
