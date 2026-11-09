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

# docs/test-plans/2026-11-08.md §4 item 1: multi-person standing session
# on_behalf_of turn isolation and dispatched-run attribution, against the
# real daemon env.sh starts (REUSE=1 reuses a running one).
#
# The daemon runs the control plane only (no agent backend is configured
# in a sandbox), so the director's turns are scripted the way the
# orchestrator records them: a task_run row with no finished_at is the
# live turn (Store.StartRun), task_observation.delivered_comment_id is
# what the turn has been shown (Store.AdvanceDelivered), and the agent's
# writes are the HTTP calls the daemon's sandbox API proxy forwards for a
# run -- the access code plus X-Grain-Acting-Task. Every attribution is
# then derived by the daemon's own Agent API gate from those rows.
#
#   item1_on_behalf_of.sh [OUT]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
OUT=${1:-$HERE/item1-on-behalf-of.out}
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
	printf '%s\t%s\t%s\t%s\n' "$res" "$1" "$2" "${4:0:200}" | tee -a "$OUT"
}
now() { date -u '+%Y-%m-%d %H:%M:%S.%N +0000 UTC'; }
last_comment_id() { sqlite3 "$DB" "select max(id) from task_comment where task_id='$1'"; }
start_turn() { # run-id delivered-comment-id
	# The daemon may itself have just dispatched the session for the new
	# message (and, with no agent backend, suspended it at once): wait for
	# no open run, then open the scripted turn (task_run_open_task allows one).
	local i
	for i in $(seq 100); do
		sqlite3 "$DB" "insert into task_run (id, task_id, sandbox, attempt, started_at) values ('$1', '$SESSION', 'scripted-director', 1, '$(now)')" 2>/dev/null && break
		sleep 0.1
	done
	[[ $i == 100 ]] && echo "could not open scripted turn $1" | tee -a "$OUT"
	sqlite3 "$DB" "insert into task_observation (task_id, delivered_comment_id) values ('$SESSION', $2)
		on conflict(task_id) do update set delivered_comment_id=excluded.delivered_comment_id"
}
finish_turn() { sqlite3 "$DB" "update task_run set finished_at='$(now)', outcome='success' where id='$1'"; }
label_of_comment() { code GET "/api/tasks/$1" >/dev/null; jq -r --arg b "$2" '[.comments[] | select(.body==$b)][-1].authorLabel // "none"' "$BODY"; }
row_of_comment() { sqlite3 "$DB" "select author_kind||':'||author_id||':'||author_source||' for '||coalesce(author_behalf_kind,'')||':'||coalesce(author_behalf_id,'')||':'||author_behalf_source from task_comment where task_id='$1' and body='$2' order by id desc limit 1"; }

SESSION=""
for _ in $(seq 100); do # the daemon files the director's task on its first cycle
	SESSION=$(code GET /api/console-sessions >/dev/null; jq -r '.sessions[] | select(.id=="director") | .taskId // empty' "$BODY")
	[[ -n $SESSION ]] && break
	sleep 0.2
done
echo "director session task: $SESSION" | tee -a "$OUT"

# The director needs write grants for the walkthrough's writes.
code GET /api/console-sessions >/dev/null
held=$(jq -c '.sessions[] | select(.id=="director") | .capabilities' "$BODY")
want=$(jq -c '. + ["api:tasks:write","api:capabilities:admin"] | unique' <<<"$held")
st=$(code PUT /api/console-sessions/director "{\"capabilities\":$want,\"expectCapabilities\":$held}")
check 1.0 "director granted api:tasks:write + api:capabilities:admin (access code, expectCapabilities)" '^200$' "$st"
for _ in $(seq 20); do
	code GET "/api/tasks/$SESSION" >/dev/null
	jq -e '.capabilities | index("api:tasks:write")' "$BODY" >/dev/null && break
	sleep 0.5
done

new_task() { # title [approved]
	code POST /api/tasks "{\"title\":\"$1\",\"repo\":\"acme/widgets\",\"approved\":${2:-true}}" >/dev/null
	jq -r .id "$BODY"
}
TARGET=$(new_task "comment target")
CLOSE1=$(new_task "close target turn 1")
CLOSE2=$(new_task "close target turn 2")

## Turn 1: alice speaks; bob speaks while the turn is answering her.
st=$(as alice@example.com POST "/api/tasks/$SESSION/comments" '{"body":"alice: please tidy the widgets backlog"}')
check 1.1 "alice messages director through IAP" '^20[01]$' "$st"
ALICE_MSG=$(last_comment_id "$SESSION")
start_turn turn-1 "$ALICE_MSG"
sleep 1
st=$(as bob@example.com POST "/api/tasks/$SESSION/comments" '{"body":"bob: and rotate the release notes"}')
check 1.2 "bob messages director mid-turn (queued for turn 2)" '^20[01]$' "$st"
BOB_MSG=$(last_comment_id "$SESSION")
sleep 3 # two reconcile ticks: the scripted turn must still be live
check 1.3 "turn 1 is still live after two reconcile ticks" '^$' "$(sqlite3 "$DB" "select finished_at from task_run where id='turn-1'")"

st=$(agent "$SESSION" POST "/api/tasks/$TARGET/comments" '{"body":"turn 1 comment"}')
check 1.4 "turn 1 comments" '^20[01]$' "$st"
check 1.5 "turn 1 comment row" '^agent:'"$SESSION"':session for human:alice@example.com:iap$' "$(row_of_comment "$TARGET" "turn 1 comment")"
check 1.6 "turn 1 comment label" '^[Dd]irector for alice@example.com$' "$(label_of_comment "$TARGET" "turn 1 comment")"
st=$(agent "$SESSION" POST /api/tasks '{"title":"filed in turn 1","repo":"acme/widgets"}')
FILED1=$(jq -r .id "$BODY")
check 1.7 "turn 1 files a task" '^20[01]$' "$st"
code GET "/api/tasks/$FILED1" >/dev/null
check 1.8 "turn 1 filed task's author label" '^[Dd]irector for alice@example.com$' "$(jq -r .authorLabel "$BODY")"
check 1.9 "turn 1 filed task's stored origin" 'alice@example.com' "$(sqlite3 "$DB" "select * from task where id='$FILED1'" | tr '\n' ' ')"
st=$(agent "$SESSION" POST "/api/tasks/$CLOSE1/close" '')
check 1.10 "turn 1 closes a task" '^20[01]$' "$st"
code GET "/api/tasks/$CLOSE1" >/dev/null
check 1.11 "turn 1 close transition label" '[Dd]irector for alice@example.com' "$(jq -r '[.transitions[] | .actorLabel // .byLabel // empty] | join(",")' "$BODY")"
st=$(agent "$SESSION" POST /api/hostds '{"name":"ws-turn-1"}')
check 1.12 "turn 1 makes a configuration write (host token)" '^20[01]$' "$st"
code GET "/api/deployment/config-audit?limit=1" >/dev/null
check 1.13 "turn 1 config-audit label" '^[Dd]irector for alice@example.com$' "$(jq -r '.[0].actorLabel' "$BODY")"
check 1.14 "turn 1 config_audit row" '^agent\|'"$SESSION"'\|session\|human\|alice@example.com\|iap$' \
	"$(sqlite3 "$DB" "select actor_kind,actor_id,actor_source,behalf_kind,behalf_id,behalf_source from config_audit order by id desc limit 1")"

## Turn 2: the orchestrator hands bob's message to the next turn.
finish_turn turn-1
start_turn turn-2 "$BOB_MSG"
agent "$SESSION" POST "/api/tasks/$TARGET/comments" '{"body":"turn 2 comment"}' >/dev/null
check 2.1 "turn 2 comment row" '^agent:'"$SESSION"':session for human:bob@example.com:iap$' "$(row_of_comment "$TARGET" "turn 2 comment")"
check 2.2 "turn 2 comment label" '^[Dd]irector for bob@example.com$' "$(label_of_comment "$TARGET" "turn 2 comment")"
agent "$SESSION" POST /api/tasks '{"title":"filed in turn 2","repo":"acme/widgets"}' >/dev/null
FILED2=$(jq -r .id "$BODY")
code GET "/api/tasks/$FILED2" >/dev/null
check 2.3 "turn 2 filed task's author label" '^[Dd]irector for bob@example.com$' "$(jq -r .authorLabel "$BODY")"
agent "$SESSION" POST "/api/tasks/$CLOSE2/close" '' >/dev/null
code GET "/api/tasks/$CLOSE2" >/dev/null
check 2.4 "turn 2 close transition label" '[Dd]irector for bob@example.com' "$(jq -r '[.transitions[] | .actorLabel // .byLabel // empty] | join(",")' "$BODY")"
agent "$SESSION" POST /api/hostds '{"name":"ws-turn-2"}' >/dev/null
code GET "/api/deployment/config-audit?limit=1" >/dev/null
check 2.5 "turn 2 config-audit label" '^[Dd]irector for bob@example.com$' "$(jq -r '.[0].actorLabel' "$BODY")"
finish_turn turn-2

## The shared access code speaks last: the idle session acts for nobody.
st=$(code POST "/api/tasks/$SESSION/comments" '{"body":"code: anything else?"}')
check 3.1 "access-code message to director" '^20[01]$' "$st"
check 3.2 "access-code message row" '^human:.*:access-code for ::$' "$(row_of_comment "$SESSION" "code: anything else?")"
agent "$SESSION" POST "/api/tasks/$TARGET/comments" '{"body":"after the code"}' >/dev/null
check 3.3 "comment answering the access code acts for nobody" '^agent:'"$SESSION"':session for ::$' "$(row_of_comment "$TARGET" "after the code")"
check 3.4 "label answering the access code" '^[Dd]irector$' "$(label_of_comment "$TARGET" "after the code")"
# A third turn shown the access code's message (live) acts for nobody too.
start_turn turn-3 "$(last_comment_id "$SESSION")"
agent "$SESSION" POST "/api/tasks/$TARGET/comments" '{"body":"turn 3 after the code"}' >/dev/null
check 3.5 "live turn shown the access code's message acts for nobody" '^agent:'"$SESSION"':session for ::$' "$(row_of_comment "$TARGET" "turn 3 after the code")"
finish_turn turn-3

## Dispatched runs act for their approver.
grant_api() { code POST "/api/tasks/$1/capabilities" '{"id":"api:tasks:write","attach":true}' >/dev/null; }
RUN_ALICE=$(new_task "run approved by alice" false)
grant_api "$RUN_ALICE"
st=$(as alice@example.com POST "/api/tasks/$RUN_ALICE/approve" '')
check 4.1 "alice approves through IAP" '^20[01]$' "$st"
agent "$RUN_ALICE" POST "/api/tasks/$TARGET/comments" '{"body":"run approved by alice speaks"}' >/dev/null
check 4.2 "alice-approved run's comment" "^agent:$RUN_ALICE:sandbox for human:alice@example.com:iap$" "$(row_of_comment "$TARGET" "run approved by alice speaks")"
agent "$RUN_ALICE" POST /api/tasks '{"title":"follow-up of alice run","repo":"acme/widgets"}' >/dev/null
FU=$(jq -r .id "$BODY")
check 4.3 "alice-approved run's filed follow-up origin" 'alice@example.com' "$(sqlite3 "$DB" "select * from task where id='$FU'" | tr '\n' ' ')"

RUN_CODE=$(new_task "run approved by the access code" false)
grant_api "$RUN_CODE"
code POST "/api/tasks/$RUN_CODE/approve" '' >/dev/null
agent "$RUN_CODE" POST "/api/tasks/$TARGET/comments" '{"body":"run approved by code speaks"}' >/dev/null
check 4.4 "access-code-approved run's comment acts for nobody" "^agent:$RUN_CODE:sandbox for ::$" "$(row_of_comment "$TARGET" "run approved by code speaks")"

# grain-approved: the director's own filing above was queued by grain
# (the session queues its proposals) -- if it was, its run acts for nobody.
RUN_PRE=$(new_task "run filed pre-approved by the access code" true)
grant_api "$RUN_PRE"
agent "$RUN_PRE" POST "/api/tasks/$TARGET/comments" '{"body":"run filed approved by code speaks"}' >/dev/null
check 4.5 "run filed pre-approved by the access code acts for nobody" "^agent:$RUN_PRE:sandbox for ::$" "$(row_of_comment "$TARGET" "run filed approved by code speaks")"

# grain-approved: the director may queue its own work (tasksApproved), so
# grain approves what it files -- even in a turn answering alice.
code PUT /api/console-sessions/director '{"tasksApproved":true}' >/dev/null
for _ in $(seq 50); do # the next cycle carries the setting onto the session's task
	code GET "/api/tasks/$SESSION" >/dev/null
	[[ $(jq -r .autoApproveProposals "$BODY") == true ]] && break
	sleep 0.2
done
st=$(as alice@example.com POST "/api/tasks/$SESSION/comments" '{"body":"alice: file one more"}')
start_turn turn-4 "$(last_comment_id "$SESSION")"
agent "$SESSION" POST /api/tasks '{"title":"queued by grain for the director","repo":"acme/widgets"}' >/dev/null
RUN_GRAIN=$(jq -r .id "$BODY")
finish_turn turn-4
code GET "/api/tasks/$RUN_GRAIN" >/dev/null
echo "grain-queued task $RUN_GRAIN approval: $(jq -c '{state,approved,approvedBy,approvedByKind,approvedByLabel}' "$BODY")" | tee -a "$OUT"
jq -r '.comments[]?.body' "$BODY" | head -c 600 | tee -a "$OUT"; echo | tee -a "$OUT"
# Observation O1 (report): on this daemon the director's filings stay
# proposed even with tasksApproved on, so 4.7 is a run nobody approved;
# grain's own approval (AutoSubmitPrincipal) is TestADispatchedRunActsForItsApprover's.
echo "INFO	4.6	director filing state with tasksApproved: $(jq -r .state "$BODY")" | tee -a "$OUT"
grant_api "$RUN_GRAIN"
agent "$RUN_GRAIN" POST "/api/tasks/$TARGET/comments" '{"body":"grain-approved run speaks"}' >/dev/null
check 4.7 "director-filed run (unapproved / grain-queued) acts for nobody" "^agent:$RUN_GRAIN:sandbox for ::$" "$(row_of_comment "$TARGET" "grain-approved run speaks")"

## Spoofing a person in the payload.
st=$(agent "$RUN_CODE" POST "/api/tasks/$TARGET/comments" '{"body":"spoof author","author":"alice@example.com"}')
check 5.1 "agent naming alice as author is refused" '^4[0-9][0-9]$' "$st"
st=$(req "$DAEMON" POST "/api/tasks/$TARGET/comments" '{"body":"spoof behalf","onBehalfOf":"alice@example.com","onBehalfOfKind":"human"}' \
	"Authorization: Bearer $CODE" "X-Grain-Acting-Task: $RUN_CODE" "X-Grain-On-Behalf-Of: alice@example.com" "X-Grain-User: alice")
check 5.2 "agent claiming onBehalfOf (body + headers) is recorded for nobody" "^agent:$RUN_CODE:sandbox for ::$" "$(row_of_comment "$TARGET" "spoof behalf")"
st=$(req "$DAEMON" POST "/api/tasks/$TARGET/comments" '{"body":"spoof iap"}' \
	"Authorization: Bearer $CODE" "X-Grain-Acting-Task: $RUN_CODE" "$IAP_HDR: $(sign_jwt alice@example.com)")
check 5.3 "agent carrying alice's IAP assertion stays the agent, for nobody" "^agent:$RUN_CODE:sandbox for ::$" "$(row_of_comment "$TARGET" "spoof iap")"

echo "TARGET=$TARGET FILED1=$FILED1 FILED2=$FILED2 CLOSE1=$CLOSE1 RUN_ALICE=$RUN_ALICE" | tee -a "$OUT"
echo "FAILS=$FAILS" | tee -a "$OUT"
exit "$FAILS"
