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

# docs/test-plans/2026-11-08.md §4 item 3a: default-branch proposal
# alignment (model.AlignDefaultBases) against the real daemon env.sh
# starts, whose GitHub (ghsim.go) says acme/widgets' default branch is
# trunk. Each parent's run calls the real propose_task tool (env.sh
# propose) with no branch.
#
#   item3_align_default_bases.sh [OUT]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
OUT=${1:-$HERE/item3-align-default-bases.out}
if [[ ${REUSE:-} == 1 ]]; then
	WORK=${WORK:-/var/tmp/attr-rails}
	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db" BODY="$WORK/body.json"
else
	start_env "${ATTR_WORK:-}" || exit 2
	trap stop_env EXIT
fi
: >"$OUT"
FAILS=0
check() { # id desc expected-regex got
	local res=PASS
	[[ $4 =~ $3 ]] || { res=FAIL; FAILS=$((FAILS + 1)); }
	printf '%s\t%s\t%s\t%s\n' "$res" "$1" "$2" "${4:0:260}" | tee -a "$OUT"
}
check 0.1 "ghsim reports trunk as acme/widgets' default branch" '"default_branch":"trunk"' "$(curl -s "http://$GHSIM/repos/acme/widgets")"

as alice@example.com POST /api/tasks '{"title":"parent on trunk","repo":"acme/widgets","base":"trunk","autoMerge":true}' >/dev/null
PT=$(jq -r .id "$BODY")
as alice@example.com POST /api/tasks '{"title":"parent on release/v1","repo":"acme/widgets","base":"release/v1","autoMerge":true}' >/dev/null
PR=$(jq -r .id "$BODY")
check 0.2 "parents stored with explicit bases and auto-merge" '^trunk\|1 release/v1\|1$' \
	"$(sqlite3 "$DB" "select base||'|'||auto_merge from task where id in ('$PT','$PR') order by cast(id as integer)" | tr '\n' ' ' | sed 's/ $//')"

ans=$(propose "$PT" '{"title":"child of the trunk parent","body":"no branch given"}')
CT=$(cat "$WORK/proposed.id")
echo "trunk parent's propose_task: $ans" | tee -a "$OUT"
code GET "/api/tasks/$CT" >/dev/null
check 1.1 "trunk child queued automatically" '^queued$' "$(jq -r .state "$BODY")"
check 1.2 "trunk child inherits auto_merge" '^true$' "$(jq -r .autoMerge "$BODY")"
check 1.3 "trunk child keeps its stored base \"\"" '^\|$' "$(sqlite3 "$DB" "select '|'||coalesce(base,'') from task where id='$CT'")"
check 1.4 "trunk child approved by grain (not a person)" '^automation:' "$(sqlite3 "$DB" "select approval_actor_kind||':'||approval_actor_id from task where id='$CT'")"

ans=$(propose "$PR" '{"title":"child of the release/v1 parent","body":"no branch given"}')
CR=$(cat "$WORK/proposed.id")
echo "release/v1 parent's propose_task: $ans" | tee -a "$OUT"
code GET "/api/tasks/$CR" >/dev/null
check 2.1 "release/v1 child held unapproved" '^proposed$' "$(jq -r .state "$BODY")"
check 2.2 "release/v1 child does not inherit auto_merge" '^(false|null)$' "$(jq -r .autoMerge "$BODY")"
note=$(jq -r '.description' "$BODY"; jq -r '[.comments[]?.body] | join("\n")' "$BODY")
check 2.3 "the [grain] note names release/v1 and the default branch" 'release/v1.*(trunk|default)|(trunk|default).*release/v1' "$(tr '\n' ' ' <<<"$note")"
echo "release/v1 child's description and comments:" >>"$OUT"
printf '%s\n' "$note" >>"$OUT"
check 2.4 "the agent was told it is held for its branch" 'same branch your task lands on' "$ans"

echo "PT=$PT PR=$PR CT=$CT CR=$CR" | tee -a "$OUT"
echo "FAILS=$FAILS" | tee -a "$OUT"
exit "$FAILS"
