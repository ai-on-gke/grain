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

# Runs after the review-context walk on the same daemon (run.sh exports
# CODE, DAEMON, WORK_DIR, RUN, STORE, ALLOW): Accept findings on the author
# task, an approval-gated (human: required, named approver) author task with
# Submit and a later push, a repository move read by unrestricted, scoped and
# ungranted callers, the 512-link / 64 KiB bounds on the task and on a linked
# review, and GET /api/tasks/{id} latency. Every step snapshots the task to
# $RUN/extras/<label>-task<id>.json and one compact line to extras.txt.
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-14-review-context/env.sh
source "$HERE/../2026-11-14-review-context/env.sh"
X=$RUN/extras
mkdir -p "$X"
AUTHOR=$(cat "$RUN/walk/author-id")
log() { echo "== $(date -u +%H:%M:%S) $*" | tee -a "$X/extras.txt"; }
compact='{id, state, gates: [(.mergeGates // [])[] | "\(.id)=\(.state)"], approvedBy: .humanApprovedBy,
  obs: (if .gate_observations == null then null else
    [ .gate_observations as $v | $v.bindings[] | select(.type == "review" or .type == "approval") as $b
      | ($v.results[] | select(.result.binding_id | endswith("/" + $b.id))) as $r
      | "\($b.type) \($b.config.semantics) \($b.config.requirement_id // ($b.config.requirement_ids | join(","))) = \($r.result.status): \($r.result.summary[0:110])" ] end),
  history: (.gate_observation_diagnostics.history // null | if . == null then null else length end)}'
snap() { # label id [curl auth header]
	local auth=${3:-"Authorization: Bearer $CODE"}
	curl -sS -H "$auth" -o "$X/$1-task$2.json" -w '%{http_code}' "$DAEMON/api/tasks/$2" >"$X/$1-task$2.code"
	{
		echo "-- $1 task $2 HTTP $(cat "$X/$1-task$2.code")"
		jq -c "$compact" "$X/$1-task$2.json" 2>/dev/null || head -c 300 "$X/$1-task$2.json"
	} | tee -a "$X/extras.txt"
}
sql() {
	if [[ $STORE == postgres ]]; then
		psql -qtA "postgres://grain:grain@127.0.0.1:5432/grain_walk?sslmode=disable" -c "$1"
	else
		sqlite3 -cmd ".timeout 10000" "$WORK_DIR/data/store/grain.db" "$1"
	fi
}
latency() { # label id: 40 sequential reads, p50/p95 in ms
	local i
	for i in $(seq 40); do
		curl -sS -o /dev/null -w '%{time_total}\n' -H "Authorization: Bearer $CODE" "$DAEMON/api/tasks/$2"
	done | sort -n | awk -v l="$1" '{a[NR]=$1*1000} END {printf "latency %s: p50=%.1fms p95=%.1fms max=%.1fms\n", l, a[int(NR*0.5)], a[int(NR*0.95)], a[NR]}' | tee -a "$X/extras.txt"
}

log "1. Accept findings on the author task $AUTHOR"
snap accept-before "$AUTHOR"
GATE=$(jq -r '[.mergeGates[]? | select(.acceptable)][0] | "\(.id) \(.reviewTask)"' "$X/accept-before-task$AUTHOR.json")
read -r G RT <<<"$GATE"
api POST "/api/tasks/$AUTHOR/accept-findings" "$(jq -n --arg g "$G" --arg r "$RT" '{gate: $g, reviewTask: $r, reason: "walkthrough: accepted as they are"}')" | tee "$X/accept-response.json" | head -c 300
echo
sleep 3
snap accept-after "$AUTHOR"
latency "author-after-walk" "$AUTHOR"

log "2. approval: base gains a sensitive human: required trigger naming alice@example.com"
tmp=$(mktemp -d)
git clone -q "$WORK_DIR/widgets.git" "$tmp/w"
mkdir -p "$tmp/w/secrets"
cat >"$tmp/w/.grain/triggers/secrets.yml" <<'YAML'
# Walkthrough: changes under secrets/ need alice's own Submit.
on: pull_request
paths: ["secrets/**"]
template: pr-review
gate: true
human: required
sensitive: true
approvers: [alice@example.com]
YAML
echo "seed" >"$tmp/w/secrets/README"
git -C "$tmp/w" add -A
git -C "$tmp/w" -c user.name=Bob -c user.email=bob@example.com commit -qm "chore: secrets/ needs alice"
git -C "$tmp/w" push -q origin main
rm -rf "$tmp"
api PUT /api/repos/acme/widgets/declarations '{"declarations": true}' >/dev/null
# Declarations are polled every 2 minutes (orchestrator/declarations.go):
# wait for the secrets rule to reach the store before filing against it.
for _ in $(seq 160); do
	[[ $(sql "SELECT COUNT(*) FROM repo_path_trigger WHERE \"trigger\" = 'secrets'" | tr -dc '0-9') -gt 0 ]] && break
	sleep 1
done
log "secrets trigger rows: $(sql "SELECT COUNT(*) FROM repo_path_trigger WHERE \"trigger\" = 'secrets'" | tr -dc '0-9')"
api GET /api/repos/acme/widgets/declarations >"$X/declarations-after.json"
NEXT=$(($(api GET '/api/tasks?limit=500' | jq '[.[].id | tonumber] | max') + 1))
cat >"$WORK_DIR/action-$NEXT.json" <<'JSON'
{"kind": "mcp_sequence", "calls": [{"tool": "run_command", "args": {"description": "approval head 1",
  "command": "cd work && echo k1 > secrets/key.txt && git add -A && git -c user.name=Walk -c user.email=walk@example.com commit -qm 'feat: add a key\n\nApproval head 1.' && git push -q origin HEAD:$(git rev-parse --abbrev-ref HEAD) 2>&1; git log --oneline -1"}}]}
JSON
APPR=$(api POST /api/tasks "$(jq -n '{title: "APPR-AUTHOR add a key under secrets/", repo: "acme/widgets", approved: true, autoMerge: true,
  description: "Walkthrough approval task: add secrets/key.txt and push."}')" | jq -r .id)
log "approval task $APPR (expected $NEXT)"
echo "$APPR" >"$X/appr-id"
for _ in $(seq 240); do
	api GET "/api/tasks/$APPR" | jq -e '[.mergeGates[]? | .id] | length > 0' >/dev/null 2>&1 && api GET "/api/tasks/$APPR" | jq -e '.state == "awaiting_submit"' >/dev/null && break
	sleep 0.5
done
sleep 8
snap appr-head1 "$APPR"
api GET "/api/tasks/$APPR" | jq '[.links[]? | select(.kind | test("approv|trigger|hold"))]' >"$X/appr-head1-links.json" 2>/dev/null || true
log "Submit by alice (the named approver) at head 1"
api POST "/api/tasks/$APPR/submit" '{}' | tee "$X/appr-submit.json" | head -c 300
echo
sleep 4
snap appr-submitted "$APPR"
log "a person pushes head 2 to the approval branch"
BR=$(api GET "/api/tasks/$APPR" | jq -r .branch)
tmp=$(mktemp -d)
git clone -q -b "$BR" "$WORK_DIR/widgets.git" "$tmp/w"
echo k2 >"$tmp/w/secrets/key.txt"
git -C "$tmp/w" -c user.name=Alice -c user.email=alice@example.com commit -qam "feat: rotate the key"
git -C "$tmp/w" push -q origin "$BR"
rm -rf "$tmp"
sleep 12
snap appr-head2 "$APPR"

log "3. repository move: task $APPR acme/widgets -> acme/gadgets"
api POST /api/repos '{"repo": "acme/gadgets"}' >"$X/add-gadgets.json" 2>&1 || true
snap move-before "$APPR"
api PATCH "/api/tasks/$APPR" '{"repo": "acme/gadgets"}' | jq -c '{id, repo, error}' | tee -a "$X/extras.txt"
sleep 2
snap move-after-unrestricted "$APPR"
# Scoped readers are external-agent tokens, which Settings must allow.
api PUT /api/settings '{"externalAgentApi": true}' >/dev/null
mint() { # name repo -> token
	api POST /api/external-agents/tokens "$(jq -n --arg n "$1" --arg r "$2" '{name: $n, scopes: ["tasks:read"], repos: [$r]}')" | tee "$X/token-$1.json" | jq -r '.token // .secret // empty'
}
NEWTOK=$(mint new-repo-reader acme/gadgets)
OLDTOK=$(mint old-repo-reader acme/widgets)
NONETOK=$(mint other-repo-reader acme/other)
[[ -n $NEWTOK ]] && snap move-after-new-repo-reader "$APPR" "Authorization: Bearer $NEWTOK"
[[ -n $OLDTOK ]] && snap move-after-old-repo-reader "$APPR" "Authorization: Bearer $OLDTOK"
[[ -n $NONETOK ]] && snap move-after-ungranted-reader "$APPR" "Authorization: Bearer $NONETOK"
[[ -n $OLDTOK ]] && snap scoped-reader-author "$AUTHOR" "Authorization: Bearer $OLDTOK"
jq -c '{repo, keys: (.gate_observation_diagnostics // {} | keys), history: (.gate_observation_diagnostics.history // [] | length)}' "$X/move-after-unrestricted-task$APPR.json" | tee -a "$X/extras.txt"
for t in $(seq 1 "$APPR"); do echo "$t $(api GET "/api/tasks/$t" | jq -c '.gate_observation_diagnostics.history // [] | length')"; done >"$X/history-lengths.txt"

log "4. bounds: 600 links on the author task"
latency "author-before-bounds" "$AUTHOR"
vals=$(for i in $(seq 600); do printf "('%s','context-manifest','walk/manifest-%03d',false)," "$AUTHOR" "$i"; done)
sql "INSERT INTO task_link (task_id, kind, target, blocks) VALUES ${vals%,}"
sleep 2
snap bounds-600-links "$AUTHOR"
latency "author-600-links" "$AUTHOR"
sql "DELETE FROM task_link WHERE task_id = '$AUTHOR' AND kind = 'context-manifest'"
log "4b. one 70000-byte link target on the author task"
big=$(head -c 70000 /dev/zero | tr '\0' 'x')
sql "INSERT INTO task_link (task_id, kind, target, blocks) VALUES ('$AUTHOR','context-manifest','walk/$big',false)"
sleep 2
snap bounds-64k "$AUTHOR"
latency "author-64k-link" "$AUTHOR"
sql "DELETE FROM task_link WHERE task_id = '$AUTHOR' AND kind = 'context-manifest'"
REV=$(jq -r '[.mergeGates[]? | .reviewTask // empty][0]' "$X/accept-after-task$AUTHOR.json")
log "4c. 600 links on linked review task $REV"
vals=$(for i in $(seq 600); do printf "('%s','context-manifest','walk/manifest-%03d',false)," "$REV" "$i"; done)
sql "INSERT INTO task_link (task_id, kind, target, blocks) VALUES ${vals%,}"
sleep 2
snap bounds-related-600 "$AUTHOR"
latency "author-related-600" "$AUTHOR"
sql "DELETE FROM task_link WHERE task_id = '$REV' AND kind = 'context-manifest'"
sleep 2
snap bounds-cleared "$AUTHOR"
log "extras done"
