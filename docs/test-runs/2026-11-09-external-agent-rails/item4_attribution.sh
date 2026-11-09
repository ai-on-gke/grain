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

# docs/test-plans/2026-11-09.md §7 item 4: an access-code credential
# naming alice@example.com (owner_source access-code, unverified) and
# one alice mints through the IAP stub (owner_source iap) each file a
# task on the real daemon; bob approves both through IAP. trailers/main.go
# reads the stored tasks and prints orchestrator.CommitTrailers -- what
# the run's commits are written with -- and a scripted fake agent commits
# with exactly those trailers to a branch of the bare repository behind
# the GitHub simulator, read back with git. The daemon runs with
# -disable-native-sandboxes, so no real agent run makes that commit (see
# the report). REUSE=1 reuses a running env.
#
#   item4_attribution.sh [OUT]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
OUT=${1:-$HERE/item4-attribution.out}
if [[ ${REUSE:-} == 1 ]]; then
	WORK=${WORK:-/var/tmp/ext-rails}
	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db" BODY="$WORK/body.json"
else
	start_env "${EXT_WORK:-}" || exit 2
	trap stop_env EXIT
fi
: >"$OUT"
FAILS=0
check() { # id desc expected-regex got
	local res=PASS
	[[ $4 =~ $3 ]] || { res=FAIL; FAILS=$((FAILS + 1)); }
	printf '%s\t%s\t%s\t%s\n' "$res" "$1" "$2" "${4:0:300}" | sed -E 's/gma_[A-Za-z0-9_]+/gma_REDACTED/g' | tee -a "$OUT"
}
mint() { # caller(email|code) body -> token
	if [[ $1 == code ]]; then code POST /api/external-agents/tokens "$2" >/dev/null; else as "$1" POST /api/external-agents/tokens "$2" >/dev/null; fi
	jq -r '.token // empty' "$BODY"
}
code PUT /api/settings '{"externalAgentApi":true}' >/dev/null
TU=$(mint code '{"name":"alice-typed","scopes":["tasks:read","tasks:write"],"repos":["acme/widgets"],"owner":"alice@example.com"}')
TV=$(mint alice@example.com '{"name":"alice-iap","scopes":["tasks:read","tasks:write"],"repos":["acme/widgets"]}')
check 4.0 "owner sources" '^alice-typed\|alice@example.com\|access-code alice-iap\|alice@example.com\|iap$' \
	"$(sqlite3 "$DB" "select name||'|'||owner||'|'||owner_source from external_agent_token where name like 'alice-%' order by name desc" | tr '\n' ' ' | sed 's/ $//')"
tok "$TU" POST /api/tasks '{"title":"filed by the unverified owner","repo":"acme/widgets"}' >/dev/null
U=$(jq -r .id "$BODY")
tok "$TV" POST /api/tasks '{"title":"filed by the iap-verified owner","repo":"acme/widgets"}' >/dev/null
V=$(jq -r .id "$BODY")
as bob@example.com POST "/api/tasks/$U/approve" '' >/dev/null
as bob@example.com POST "/api/tasks/$V/approve" '' >/dev/null
for id in "$U" "$V"; do
	code GET "/api/tasks/$id" >/dev/null
	echo "task $id as the UI reads it: $(jq -c '{creator, author, authorKind, authorLabel, owner, approvedBy, approvedByLabel}' "$BODY")" | tee -a "$OUT"
done

(cd "$ROOT" && go run "$HERE/trailers/main.go" -store "$WORK/data/store" "$U" "$V") >"$WORK/trailers.out" 2>&1
cat "$WORK/trailers.out" >>"$OUT"
TRU=$(awk -F'\t' -v id="$U" '$1==id {print $2}' "$WORK/trailers.out")
TRV=$(awk -F'\t' -v id="$V" '$1==id {print $2}' "$WORK/trailers.out")

# The scripted fake agent: one commit per task on its branch, with the
# trailers the orchestrator would write, pushed to the simulator's repo.
fake_commit() { # id trailers
	local tmp
	tmp=$(mktemp -d)
	git clone -q "$WORK/widgets.git" "$tmp" 2>/dev/null
	echo "$1" >"$tmp/task-$1.txt"
	git -C "$tmp" add . >/dev/null
	local msg="fix: task $1"$'\n\n'"Fake agent commit."$'\n\n'"$(sed 's/ | /\n/g' <<<"$2")"
	git -C "$tmp" -c user.name=fake-agent -c user.email=fake-agent@example.com commit -qm "$msg"
	git -C "$tmp" push -q origin "HEAD:refs/heads/grain/task-$1" 2>/dev/null
	rm -rf "$tmp"
	git -C "$WORK/widgets.git" log -1 --format=%B "grain/task-$1" | git interpret-trailers --parse
}
CU=$(fake_commit "$U" "$TRU")
CV=$(fake_commit "$V" "$TRV")
printf 'pushed commit trailers, task %s:\n%s\npushed commit trailers, task %s:\n%s\n' "$U" "$CU" "$V" "$CV" >>"$OUT"
check 4.1 "unverified owner: no Requested-by" '^[^R]*$' "$(grep -c '^Requested-by' <<<"$CU" | sed 's/^0$//')"
check 4.2 "unverified owner: Approved-by bob" 'Approved-by: bob@example.com' "$CU"
check 4.3 "verified owner: Requested-by alice" 'Requested-by: alice@example.com' "$CV"
check 4.4 "verified owner: Approved-by bob" 'Approved-by: bob@example.com' "$CV"
echo "FAILS=$FAILS" | tee -a "$OUT"
exit "$FAILS"
