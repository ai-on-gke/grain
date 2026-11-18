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

# docs/test-plans/2026-11-18.md §3 on a real daemon (STORE=sqlite|postgres).
# Every author task touches payments/** (a template-less sensitive trigger),
# so its head gets a targeted review whose findings hold the targeted-review
# gate; rules.json (gen-rules.py) scripts what each run does.
#
#   A1-A3  accepted in a browser: /next/ at 1440 and 390 px, classic /tasks/<id>
#   A4     payments + ledger: one review serving two triggers; trigger:payments
#          accepted, then the rest with -gate targeted-review (CLI)
#   A5     + frozen/** (auto_merge: never): withdrawn merge policy stands
#   A6/A7  a QA hold / broken-rules hold set on the task, then accepted
#   A8     an agent token tries; a gate with no findings; review,
#          security-review
#   A9     the review's draft refused with GitHub's 422 (githubsim
#          RefuseReviews): owner asked, replies, next review reads [answered]
#   A10    four reviews: 15 long answered questions, 2 answered, 3 open, a
#          person's force-push; R4's prompt shows the cap order and footers
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
STORE=${STORE:-sqlite}
OUT=${OUT:-$HERE/out/$STORE}
UI=${UI:-1}
rm -rf "$OUT"
mkdir -p "$OUT"
init_env /home/debian/walk-rg
if [[ $STORE == postgres ]]; then
	psql -q "postgres://grain:grain@127.0.0.1:5432/postgres?sslmode=disable" \
		-c "DROP DATABASE IF EXISTS grain_walk WITH (FORCE)" -c "CREATE DATABASE grain_walk"
fi
cp "$HERE/rules.json" "$WORK_DIR/rules.json"
echo '{"kind": "nothing_needed", "reason": "walkthrough default completion"}' >"$WORK_DIR/action-default.json"
touch "$WORK_DIR/no-session"
start_ghsim "$WORK_DIR"
start_daemon "$GRAIN" "$WORK_DIR"
log() { echo "== $(date -u +%H:%M:%S) $*" | tee -a "$OUT/steps.log" >&2; }
note() { echo "$*" | tee -a "$OUT/steps.log" >&2; }

api PUT /api/repos/acme/widgets/declarations '{"declarations": true}' >/dev/null
for _ in $(seq 120); do
	api GET /api/repos/acme/widgets/declarations | grep -q payments && break
	sleep 0.5
done

state() { api GET "/api/tasks/$1" | jq -r .state; }
gates() { api GET "/api/tasks/$1" | jq -c '{autoMerge, gates: [.mergeGates[]? | {id, state, reviewTask, acceptable, detail: (.detail // "" | .[0:60])}], findings: [.reviewFindings[]? | {gate, reviewTask, summary: .summary[0:40]}], questions: .reviewQuestions}'; }
author() { # tag -> id
	api POST /api/tasks "$(jq -n --arg t "WALK-$1 tweak payments" '{title: $t, repo: "acme/widgets", approved: true, autoMerge: true,
	  description: "Walkthrough author task: edit payments/ and push."}')" | jq -r .id
}
reviews_of() { api GET '/api/tasks?limit=300' | jq -r --arg t "WALK-$1 " '.[] | select((.title|startswith("Targeted review")) and (.title|contains($t))) | .id' | sort -n; }
kick() { # undefer every targeted review still deferred (reviewDebounce is 3m)
	local id
	for id in $(api GET '/api/tasks?limit=300' | jq -r '.[] | select(.title|startswith("Targeted review")) | select(.state != "closed") | .id'); do
		api POST "/api/tasks/$id/undefer" '{}' >/dev/null 2>&1 || true
	done
}
wait_reviews() { # tag count
	for _ in $(seq 360); do
		[[ $(reviews_of "$1" | wc -l) -ge $2 ]] && return 0
		kick
		sleep 0.5
	done
	log "only $(reviews_of "$1" | wc -l) reviews of $1, wanted $2"
	return 1
}
wait_closed() { # id
	for _ in $(seq 360); do
		[[ $(state "$1") == closed ]] && return 0
		kick
		sleep 0.5
	done
	log "task $1 never closed (is $(state "$1"))"
	return 1
}
wait_gate() { # author gate state
	for _ in $(seq 120); do
		[[ $(api GET "/api/tasks/$1" | jq -r --arg g "$2" '.mergeGates[]? | select(.id==$g) | .state') == "$3" ]] && return 0
		sleep 0.5
	done
	log "task $1 gate $2 never reached $3: $(gates "$1")"
	return 1
}
with_findings() { # tag -> author id, once its first targeted review has left findings
	local a r
	a=$(author "$1")
	wait_reviews "$1" 1
	r=$(reviews_of "$1" | tail -1)
	wait_closed "$r"
	wait_gate "$a" targeted-review findings
	echo "$a"
}
accept() { # id body -> HTTP status, response in $OUT/accept-<id>-<n>.json
	local n=1
	while [[ -f "$OUT/accept-$1-$n.json" ]]; do n=$((n + 1)); done
	curl -sS -o "$OUT/accept-$1-$n.json" -w '%{http_code}' -X POST -H "Authorization: Bearer $CODE" \
		-H "Content-Type: application/json" -d "$2" "$DAEMON/api/tasks/$1/accept-findings"
}
cli() { GRAIN_SERVER="$DAEMON" GRAIN_TOKEN="$CODE" "$GRAIN" "$@" 2>&1; }
sql() { # statement, against this store
	if [[ $STORE == postgres ]]; then
		psql -q -At "${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk?sslmode=disable}" -c "$1"
	else
		sqlite3 "$DB" "$1"
	fi
}
head_of() { git -C "$WORK_DIR/widgets.git" rev-parse "refs/heads/$(api GET "/api/tasks/$1" | jq -r .branch)"; }
ui() { # mode width task
	[[ $UI == 1 ]] || return 0
	(cd "$ROOT/ui-ng" && GRAIN_URL="$DAEMON" GRAIN_CODE="$CODE" OUT="$OUT/shots" MODE=$1 WIDTH=$2 TASK=$3 \
		node "$HERE/ui-accept.mjs") | tee -a "$OUT/ui.jsonl" || true
}

# ---- Item 1: targeted review end to end, accepted in the UI ----
log "item 1: A1-A3 (one finding each)"
A1=$(with_findings A1)
A2=$(with_findings A2)
A3=$(with_findings A3)
for a in $A1 $A2 $A3; do note "task $a before: $(gates "$a")"; done
log "A1 accepted on /next/ at 1440 px"
ui next 1440 "$A1"
log "A2 accepted on /next/ at 390 px"
ui next 390 "$A2"
log "A3 accepted on classic /tasks/$A3"
ui classic 1440 "$A3"
sleep 2
for a in $A1 $A2 $A3; do note "task $a after: $(gates "$a")"; done

# ---- Item 1b: two triggers, one accepted ----
log "item 1b: A4 touches payments and ledger"
A4=$(with_findings A4)
note "A4 before: $(gates "$A4")"
note "A4 accept trigger:payments (CLI): $(cli accept-findings -gate trigger:payments "$A4" 'payments rounding is fine')"
note "A4 after trigger:payments: $(gates "$A4")"
note "A4 accept trigger:ledger (CLI): $(cli accept-findings -gate trigger:ledger "$A4" 'ledger check is upstream' | head -1)"
note "A4 after trigger:ledger: $(gates "$A4")"
note "A4 accept targeted-review (CLI): $(cli accept-findings -gate targeted-review "$A4" 'anything left' | head -1)"

# ---- Item 2: holds block restoration ----
log "item 2: holds"
A5=$(with_findings A5)
note "A5 (frozen/**, auto_merge: never) before: $(gates "$A5")"
note "A5 links: $(sql "select kind||'='||substr(target,1,12) from task_link where task_id='$A5' and kind in ('merge-policy-withdrawn','qa-hold','broken-rules-hold')" | tr '\n' ' ')"
note "A5 accept: $(accept "$A5" '{"gate":"targeted-review","reason":"fine as is"}')"
note "A5 after: $(gates "$A5")"
sleep 5
note "A5 5s later: $(gates "$A5") links: $(sql "select kind from task_link where task_id='$A5' and kind like 'merge-policy%'" | tr '\n' ' ')"
A6=$(with_findings A6)
sql "insert into task_link (task_id, kind, target, blocks) values ('$A6', 'qa-hold', '$(head_of "$A6")', false)"
note "A6 qa-hold set: $(sql "select kind from task_link where task_id='$A6' and kind='qa-hold'")"
note "A6 accept: $(accept "$A6" '{"gate":"targeted-review","reason":"fine as is"}')"
note "A6 after: $(gates "$A6") qa-hold still: $(sql "select count(*) from task_link where task_id='$A6' and kind='qa-hold'")"
A7=$(with_findings A7)
sql "insert into task_link (task_id, kind, target, blocks) values ('$A7', 'broken-rules-hold', '$(head_of "$A7")', false)"
note "A7 accept: $(accept "$A7" '{"gate":"targeted-review","reason":"fine as is"}')"
note "A7 after: $(gates "$A7") broken-rules-hold still: $(sql "select count(*) from task_link where task_id='$A7' and kind='broken-rules-hold'")"

# ---- Item 3: CLI and authority ----
log "item 3: authority"
A8=$(with_findings A8)
echo "$A8" >/home/debian/walk-rg/a8-id
AG=$(api POST /api/tasks "$(jq -n '{title: "WALK-AGENT try to accept", no_repo: true, noRepo: true, approved: true,
  grants: ["https://grain.dev/capabilities/api#tasks:write"], description: "Walkthrough: an agent tries accept-findings."}')" | jq -r '.id // .error // .')
note "agent task: $AG"
wait_closed "$AG" || true
note "agent attempt output: $(jq -r '.outputs[0].text' "$WORK_DIR/runs/mcp-$AG.jsonl" 2>/dev/null | head -c 600)"
note "A8 after the agent: $(gates "$A8")"
note "owner, gate with no findings (security-review): $(cli accept-findings -gate security-review "$A8" 'nothing there')"
note "owner, gate with no findings (review): $(cli accept-findings -gate review "$A8" 'nothing there')"
note "owner, no reason: $(cli accept-findings -gate targeted-review "$A8")"
note "owner, stale head: $(cli accept-findings -gate targeted-review -head 0000000 "$A8" 'stale')"
note "owner accepts A8: $(cli accept-findings -gate targeted-review "$A8" 'fine as is' | head -1)"
note "owner, already accepted: $(cli accept-findings -gate targeted-review "$A8" 'again')"

# ---- Item 4: 422 fallback ----
log "item 4: A9, draft review refused with a 422"
curl -sS "http://$GHSIM/_sim/refuse-reviews?n=1" >>"$OUT/sim-controls.log"
A9=$(with_findings A9)
R9=$(reviews_of A9 | tail -1)
note "A9 after R9: state=$(state "$A9") $(gates "$A9")"
curl -sS "http://$GHSIM/_sim/reviews" >"$OUT/a9-sim.json"
note "sim reviews for A9's PR: $(jq -c '[.reviews[]? | {Number, n: (.Comments|length)}] | .[-3:]' "$OUT/a9-sim.json")"
api GET "/api/tasks/$A9" | jq -r '.comments[]? | "--- \(.author|tostring|.[0:60])\n\(.body)"' >"$OUT/a9-comments-before.md"
api GET "/api/inbox" >"$OUT/inbox.json" 2>/dev/null || true
ui inbox 1440 "$A9"
ui inbox 390 "$A9"
api POST "/api/tasks/$A9/comments" '{"body": "Q9: yes, log the refused amount. Go ahead."}' >"$OUT/a9-reply.json"
for _ in $(seq 120); do [[ -f "$WORK_DIR/runs/prompt-$A9-2.txt" ]] && break; sleep 0.5; done
note "A9 requeued by the reply: $([[ -f "$WORK_DIR/runs/prompt-$A9-2.txt" ]] && echo yes || echo "no (state $(state "$A9"))")"
wait_reviews A9 2 || true
R9b=$(reviews_of A9 | tail -1)
wait_closed "$R9b" || true
prompt_of() { jq -r '.message.content' "$WORK_DIR/runs/prompt-$1-1.txt" 2>/dev/null; } # the stream-json event fake-agy was handed
earlier_of() { awk '/^## Earlier reviews of this pull request/{p=1;next} p&&/^(## |\*\*This is an incremental|Work in )/{exit} p'; }
prompt_of "$R9b" >"$OUT/a9-second-review-prompt.md"
earlier_of <"$OUT/a9-second-review-prompt.md" >"$OUT/a9-second-review-earlier.md"
note "R9b=$R9b earlier reviews: $(grep -c '\[answered\]' "$OUT/a9-second-review-earlier.md") [answered] line(s): $(grep '\[answered\]' "$OUT/a9-second-review-earlier.md" | head -c 160)"

# ---- Item 5: question cap ----
log "item 5: A10 over four reviews"
A10=$(author A10)
for n in 1 2 3; do
	wait_reviews A10 "$n"
	R=$(reviews_of A10 | tail -1)
	wait_closed "$R"
	sleep 3
	note "A10 R$n=$R closed: $(gates "$A10" | head -c 300)"
	if [[ $n -lt 3 ]]; then
		api POST "/api/tasks/$A10/comments" "$(jq -n --arg n "$n" '{body: ("Answers to review " + $n + ": all fine, carry on.")}')" >>"$OUT/a10-replies.json"
		for _ in $(seq 120); do [[ -f "$WORK_DIR/runs/prompt-$A10-$((n + 1)).txt" ]] && break; sleep 0.5; done
	fi
done
log "a person force-pushes A10's branch"
tmp=$(mktemp -d)
BR=$(api GET "/api/tasks/$A10" | jq -r .branch)
git clone -q -b "$BR" "$WORK_DIR/widgets.git" "$tmp/w"
git -C "$tmp/w" reset -q --soft HEAD~1
echo "// person" >>"$tmp/w/payments/pay.go"
git -C "$tmp/w" -c user.name=Alice -c user.email=alice@example.com commit -qam "feat: person rewrites head 3"
git -C "$tmp/w" push -q -f origin "$BR"
rm -rf "$tmp"
wait_reviews A10 4
R4=$(reviews_of A10 | tail -1)
wait_closed "$R4" || true
prompt_of "$R4" >"$OUT/a10-r4-prompt.md"
earlier_of <"$OUT/a10-r4-prompt.md" >"$OUT/a10-r4-earlier.md"
QB=$(grep -E '^ +- (\[(open|answered)\]|\.\.\. \()' "$OUT/a10-r4-earlier.md" | sed -E 's/^ +- //' | LC_ALL=C awk '{n+=length($0)} END{print n+0}')
note "R4=$R4 earlier reviews: $(grep -c '^- Task' "$OUT/a10-r4-earlier.md") reviews, $(grep -c '\[open\]' "$OUT/a10-r4-earlier.md") open + $(grep -c '\[answered\]' "$OUT/a10-r4-earlier.md") answered listed, ${QB} B of question lines and footers (cap 2048)"
grep -E '^- Task|omitted' "$OUT/a10-r4-earlier.md" | cut -c1-140 | while read -r l; do note "  $l"; done

cp "$WORK_DIR"/runs/mcp-*.jsonl "$OUT/" 2>/dev/null || true
grep -E "review|422|pending review|accept" "$WORK_DIR/daemon.log" | grep -v " ci: " >"$OUT/daemon-review.log" || true
api GET '/api/tasks?limit=300' | jq -r '.[] | "\(.id) \(.state) \(.title)"' >"$OUT/tasks.txt"
log "done A1=$A1 A2=$A2 A3=$A3 A4=$A4 A5=$A5 A6=$A6 A7=$A7 A8=$A8 AG=$AG A9=$A9 A10=$A10"
