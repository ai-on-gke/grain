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

# docs/test-plans/2026-11-15.md §2 against a live `grain daemon` over
# SQLite or PostgreSQL (STORE=sqlite|postgres). Each scenario prints one
# RESULT line and leaves its evidence in $WORK_DIR/captures/.
#
#   STORE=postgres GRAIN=/home/debian/bin/grain ./run_walkthrough.sh [scenario...]
#
# Scenarios: a (needs DAEMON_ARGS="-max-mergers 1") c c_person e e_person backstop_never backstop_unreadable
# backstop_nomode backstop_then_sendback close_reopen console (default: all).

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
set +e

WORK=${WORK:-/tmp/walk-rl-${STORE:-sqlite}}
CAP() { echo "$WORK/captures/${STORE:-sqlite}-$1"; }
result() { echo "RESULT [${STORE:-sqlite}] $*" | tee -a "$WORK/captures/results.txt"; }

# left_deferred_within waits up to $2 seconds for task $1 to leave
# deferred and prints how long it took ("" if it never did).
left_deferred_within() {
	local id=$1 secs=$2 start
	start=$(date +%s.%N)
	for _ in $(seq $((secs * 4))); do
		if [[ $(state_of "$id") != deferred ]]; then
			python3 -c "print(f'{$(date +%s.%N) - $start:.1f}s')"
			return 0
		fi
		sleep 0.25
	done
	return 1
}

snapshot() {
	local id=$1 label=$2
	api GET "/api/tasks/$id" >"$(CAP "$label-task.json")"
	jq -r '(.comments // [])[] | "--- \(.authorKind) \(.createdAt)\n\(.body)"' "$(CAP "$label-task.json")" >"$(CAP "$label-conversation.txt")"
	jq -r '(.transitions // [])[] | "\(.at) \(.state)"' "$(CAP "$label-task.json")" >"$(CAP "$label-transitions.txt")"
}

finish_with() {
	cat >"$WORK/action-$1.json" <<EOF
[{"kind": "comment_on_issue", "comment": "$2"}]
EOF
}

# sql_ts prints now minus $1 (a python timedelta expression) in the form
# both stores parse.
sql_ts() {
	python3 -B -c "import datetime as d; print((d.datetime.now(d.timezone.utc) - d.timedelta($1)).strftime('%Y-%m-%d %H:%M:%S+00:00'))"
}

# backdate moves task $1's resume_later -- its run, deferral, suspension and
# comment -- 6h05m into the past, past the 6h CI wake backstop.
backdate() {
	local id=$1 started finished deferred
	started=$(sql_ts "hours=6, minutes=6")
	finished=$(sql_ts "hours=6, minutes=5, seconds=-5")
	deferred=$(sql_ts "hours=6, minutes=5")
	store_exec "UPDATE task_run SET started_at='$started', finished_at='$finished' WHERE task_id='$id' AND finished_at IS NOT NULL;" >/dev/null
	store_exec "UPDATE task SET deferred_at='$deferred' WHERE id='$id';" >/dev/null
	store_exec "UPDATE task_suspension SET created_at='$deferred' WHERE task_id='$id';" >/dev/null
	store_exec "UPDATE task_comment SET created_at='$deferred' WHERE task_id='$id' AND body LIKE 'grain: this task''s run stopped with resume_later%';" >/dev/null
}

stalled_notifications() {
	api GET "/api/notifications" | jq -c --arg id "$1" '[.[] | select(.subject == "pipeline.ci-wake-stalled:" + $id) | {open: (.clearedAt == null), clearedAt}]'
}

attempts_of() { api GET "/api/tasks/$1" | jq -r '(.attempts // []) | map(.outcome) | join(",")'; }

ci_wait_ids() { api GET "/api/sandboxes" | jq -c '[(.ciWaits // [])[].taskId]'; }

# --- 1(c): a new unresolved review thread -------------------------------
scenario_c() {
	local id pr
	id=$(park_on_ci "Walk c review thread") || { result "c: FAIL could not park"; return; }
	pr=$(curl -fsS "http://$GHSIM/_sim/pulls" | jq -r --arg b "task-$id-" '.[] | select(.head | contains($b)) | .number')
	finish_with "$id" "answered the review thread"
	curl -fsS -X POST "http://$GHSIM/_sim/review-comment" \
		-d "{\"pr\":$pr,\"id\":$((70000 + id)),\"user\":\"bob\",\"body\":\"Please rename this variable.\",\"path\":\"walk.txt\",\"line\":1}" >/dev/null
	# Review comments are read on github.PollSchedule's cadence: every 30s
	# (PollBase) while the pull request is new, backing off to 8m (PollCap).
	local took
	if took=$(left_deferred_within "$id" 120); then
		wait_state "$id" "awaiting_submit|completed|awaiting_reply" 60 >/dev/null
		snapshot "$id" c
		local said logged
		said=$(grep -c "review thread" "$(CAP c-conversation.txt)")
		logged=$(grep -c "task $id was resting on resume_later; new review threads sends it back" "$WORK/daemon.log")
		result "c: PASS task $id (PR #$pr) left deferred after $took, runs=$(attempts_of "$id"), conversation mentions review thread x$said, daemon log release lines=$logged"
	else
		snapshot "$id" c
		result "c: FAIL task $id (PR #$pr) still $(state_of "$id") 120s after a new review thread"
	fi
}

# --- 1(a): a merge-queue repair -----------------------------------------
# Tasks 4523/4526's shape: an auto-merge change the merge queue finds
# conflicted is sent a repair; that repair run rests on
# resume_later(ci: my-branch) without fixing anything; the queue's next
# attempt (askForRepair) has to take it off the wait. Needs a merger:
# DAEMON_ARGS="-max-mergers 1".
scenario_a() {
	local id pr
	cat >"$WORK/action-default.json" <<'EOF'
[{"kind": "mcp_sequence", "calls": [
   {"tool": "run_command", "args": {"command": "cd work && date +%s%N > walk.txt && git add walk.txt && git commit -qm 'chore: touch walk.txt' -m 'Walkthrough commit.' && git push -q origin HEAD", "description": "pushing the branch"}},
   {"tool": "comment_on_issue", "args": {"comment": "pushed the change"}}]}]
EOF
	seed_main_ci
	id=$(api POST "/api/tasks" '{"title":"Walk a merge-queue repair","repo":"acme/widgets","approved":true,"autoMerge":true}' | jq -r '.id')
	wait_state "$id" "completed|awaiting_submit" 90 >/dev/null || { result "a: FAIL task $id never finished its first run ($(state_of "$id"))"; return; }
	rm -f "$WORK/action-default.json"
	pr=$(curl -fsS "http://$GHSIM/_sim/pulls" | jq -r --arg b "task-$id-" '.[] | select(.head | contains($b)) | .number')
	cat >"$WORK/action-$id.json" <<'EOF'
[{"kind": "resume_later", "mcp_args": {"reason": "repair pushed nothing yet; waiting for CI", "when": {"ci": "my-branch"}}},
 {"kind": "comment_on_issue", "comment": "resolved the conflict"}]
EOF
	curl -fsS "http://$GHSIM/_sim/mergeable?pr=$pr&value=false" >/dev/null
	if ! wait_state "$id" deferred 120 >/dev/null; then
		snapshot "$id" a
		result "a: FAIL task $id (PR #$pr) never rested on resume_later in its repair run ($(state_of "$id"))"
		return
	fi
	local deferred_at
	deferred_at=$(date -u +%T)
	local took=""
	took=$(left_deferred_within "$id" 240) || took="never"
	wait_state "$id" "completed|awaiting_submit|awaiting_reply" 60 >/dev/null
	snapshot "$id" a
	result "a: task $id (PR #$pr) repair run rested deferred at $deferred_at; the queue's next repair released it after $took; attempts=$(attempts_of "$id"); release log lines=$(grep -c "task $id was resting on resume_later; the merge queue's repair" "$WORK/daemon.log")"
}

# --- 1(c) control: a person's Defer survives a new review thread --------
scenario_c_person() {
	local id pr
	id=$(park_on_ci "Walk c person defer") || { result "c_person: FAIL could not park"; return; }
	pr=$(curl -fsS "http://$GHSIM/_sim/pulls" | jq -r --arg b "task-$id-" '.[] | select(.head | contains($b)) | .number')
	sleep 1.2
	api POST "/api/tasks/$id/defer" '{}' >/dev/null
	curl -fsS -X POST "http://$GHSIM/_sim/review-comment" \
		-d "{\"pr\":$pr,\"id\":$((70000 + id)),\"user\":\"bob\",\"body\":\"Another nit.\",\"path\":\"walk.txt\",\"line\":1}" >/dev/null
	if left_deferred_within "$id" 15 >/dev/null; then
		snapshot "$id" c_person
		result "c_person: FAIL task $id (person-deferred) left deferred: $(state_of "$id")"
	else
		snapshot "$id" c_person
		result "c_person: PASS task $id stayed deferred 15s after a review thread; runs=$(attempts_of "$id")"
	fi
	api POST "/api/tasks/$id/close" '{}' >/dev/null
}

# seed_review_questions records a review-questions hold on task $1 (as
# noteFindingsOnReviewedTask writes it) after its latest comment, naming a
# stand-in review task.
seed_review_questions() {
	local id=$1 review after
	review=$(api POST "/api/tasks" '{"title":"Stand-in review","repo":"acme/widgets"}' | jq -r '.id')
	after=$(store_exec_value "SELECT MAX(id) FROM task_comment WHERE task_id='$id';")
	store_exec "INSERT INTO task_link (task_id, kind, target, blocks) VALUES ('$id', 'review-questions', '$review@$after', false);" >/dev/null
	echo "$review"
}

store_exec_value() {
	if [[ ${STORE:-sqlite} == sqlite ]]; then
		sqlite3 -cmd ".timeout 5000" "$WORK_DIR/data/store/grain.db" "$1"
	else
		PGPASSWORD=grain psql -qtA -h 127.0.0.1 -U grain -d grain_walk -c "$1"
	fi
}

# --- 1(e): a review-questions decision ----------------------------------
scenario_e() {
	local id review
	id=$(park_on_ci "Walk e review decision") || { result "e: FAIL could not park"; return; }
	review=$(seed_review_questions "$id")
	finish_with "$id" "carried out the owner's decision"
	api POST "/api/tasks/$id/comments" '{"body":"Agreed: keep it core-only."}' >/dev/null
	local took
	if took=$(left_deferred_within "$id" 30); then
		wait_state "$id" "awaiting_submit|completed|awaiting_reply" 60 >/dev/null
		snapshot "$id" e
		result "e: PASS task $id left deferred after $took on its owner's answer (review $review), runs=$(attempts_of "$id"), release lines=$(grep -c "task $id was resting on resume_later; its owner's review decision" "$WORK/daemon.log"), says decision x$(grep -c "decision" "$(CAP e-conversation.txt)")"
	else
		snapshot "$id" e
		result "e: FAIL task $id still $(state_of "$id") 30s after its owner answered the review's questions"
	fi
}

scenario_e_person() {
	local id
	id=$(park_on_ci "Walk e person defer") || { result "e_person: FAIL could not park"; return; }
	sleep 1.2
	api POST "/api/tasks/$id/defer" '{}' >/dev/null
	seed_review_questions "$id" >/dev/null
	api POST "/api/tasks/$id/comments" '{"body":"Agreed: keep it core-only."}' >/dev/null
	if left_deferred_within "$id" 15 >/dev/null; then
		result "e_person: FAIL task $id (person-deferred) left deferred: $(state_of "$id")"
	else
		result "e_person: PASS task $id stayed deferred 15s after its owner's answer"
	fi
	snapshot "$id" e_person
	api POST "/api/tasks/$id/close" '{}' >/dev/null
}

# --- 2: the 6h backstop --------------------------------------------------
backstop_common() {
	local label=$1 id=$2
	finish_with "$id" "woken by the backstop"
	backdate "$id"
	local took
	if took=$(left_deferred_within "$id" 30); then
		local raised
		raised=$(stalled_notifications "$id")
		wait_state "$id" "awaiting_submit|completed|awaiting_reply" 60 >/dev/null
		snapshot "$id" "$label"
		local after_run
		after_run=$(stalled_notifications "$id")
		api POST "/api/tasks/$id/close" '{}' >/dev/null
		sleep 2
		result "$label: PASS task $id woke $took after back-dating 6h05m; ci-wake-stalled right after wake: $raised; after its run: $after_run; after close: $(stalled_notifications "$id"); says backstop x$(grep -c "backstop" "$(CAP "$label-conversation.txt")")"
	else
		snapshot "$id" "$label"
		result "$label: FAIL task $id still $(state_of "$id") 30s after back-dating its wait 6h05m"
	fi
}

scenario_backstop_never() {
	local id
	id=$(park_on_ci "Walk backstop never") || { result "backstop_never: FAIL could not park"; return; }
	backstop_common backstop_never "$id"
}

# The verdict cannot be read: GitHub (the sim) is down for the whole wait.
scenario_backstop_unreadable() {
	local id
	id=$(park_on_ci "Walk backstop unreadable") || { result "backstop_unreadable: FAIL could not park"; return; }
	kill "$(cat "$WORK/ghsim.pid")"
	sleep 1
	backstop_common backstop_unreadable "$id"
	grep -E "checking CI wake for task $id" "$WORK/daemon.log" | tail -1 >"$(CAP backstop_unreadable-error.txt)"
	start_ghsim "$WORK"
}

# The repository's CI mode cannot be resolved: the task is moved onto a
# repository whose stored settings row cannot be read (a text value in an
# INTEGER column, which SQLite's type affinity accepts and the scan into a
# bool refuses), so model.Store.TaskCIMode fails every cycle. PostgreSQL's
# typed columns refuse that row, so the variant is SQLite's alone.
scenario_backstop_nomode() {
	if [[ ${STORE:-sqlite} != sqlite ]]; then
		result "backstop_nomode: SKIP on ${STORE} (no way to store an unreadable repo_config row without breaking every task)"
		return
	fi
	local id
	id=$(park_on_ci "Walk backstop no mode") || { result "backstop_nomode: FAIL could not park"; return; }
	store_exec "INSERT INTO repo_config (owner, name, default_capabilities, merge_on_green) VALUES ('acme', 'ghost-widgets', '[]', 'yes');" >/dev/null
	store_exec "UPDATE task SET target_name='ghost-widgets' WHERE id='$id';" >/dev/null
	backstop_common backstop_nomode "$id"
	grep -E "task $id's CI wake .* cannot be resolved" "$WORK/daemon.log" | tail -1 >"$(CAP backstop_nomode-error.txt)"
	result "backstop_nomode: error the wake reported: $(cut -c1-300 "$(CAP backstop_nomode-error.txt)")"
	store_exec "DELETE FROM repo_config WHERE name='ghost-widgets';" >/dev/null
}

# The backstop's ci-wake-stalled notification is raised as it wakes the
# task; the run it sends rests on resume_later again, and a new review
# thread then takes it off that wait, which clears the notification
# (sendBackFromResumeLater). Closing a task clears its own too.
scenario_backstop_then_sendback() {
	local id pr
	id=$(park_on_ci "Walk backstop then send-back") || { result "backstop_then_sendback: FAIL could not park"; return; }
	pr=$(curl -fsS "http://$GHSIM/_sim/pulls" | jq -r --arg b "task-$id-" '.[] | select(.head | contains($b)) | .number')
	cat >"$WORK/action-$id.json" <<'EOF'
[{"kind": "resume_later", "mcp_args": {"reason": "still waiting for CI", "when": {"ci": "my-branch"}}},
 {"kind": "comment_on_issue", "comment": "answered the thread"}]
EOF
	backdate "$id"
	left_deferred_within "$id" 30 >/dev/null
	local raised
	raised=$(stalled_notifications "$id")
	wait_state "$id" deferred 60 >/dev/null
	local parked_again
	parked_again=$(state_of "$id")
	curl -fsS -X POST "http://$GHSIM/_sim/review-comment" \
		-d "{\"pr\":$pr,\"id\":$((70000 + id)),\"user\":\"bob\",\"body\":\"One more thing.\",\"path\":\"walk.txt\",\"line\":1}" >/dev/null
	# The pull request has been polled idle since the task first parked, so
	# its review comments are read on the backed-off cadence (up to
	# github.PollCap, 8m), as for any finished pull request.
	local took=""
	took=$(left_deferred_within "$id" 540) || took="never"
	sleep 1
	snapshot "$id" backstop_then_sendback
	result "backstop_then_sendback: task $id ci-wake-stalled after backstop=$raised; re-parked=$parked_again; review thread released it after $took; ci-wake-stalled now=$(stalled_notifications "$id")"
}

# --- 3: closed and reopened ---------------------------------------------
scenario_close_reopen() {
	local id
	id=$(park_on_ci "Walk close reopen") || { result "close_reopen: FAIL could not park"; return; }
	# Past CIWaitHealthAge (1h) so grain health and /api/sandboxes list it.
	local started finished deferred
	started=$(sql_ts "hours=2, minutes=1")
	finished=$(sql_ts "hours=2, seconds=-5")
	deferred=$(sql_ts "hours=2")
	store_exec "UPDATE task_run SET started_at='$started', finished_at='$finished' WHERE task_id='$id' AND finished_at IS NOT NULL;" >/dev/null
	store_exec "UPDATE task SET deferred_at='$deferred' WHERE id='$id';" >/dev/null
	store_exec "UPDATE task_suspension SET created_at='$deferred' WHERE task_id='$id';" >/dev/null
	store_exec "UPDATE task_comment SET created_at='$deferred' WHERE task_id='$id' AND body LIKE 'grain: this task''s run stopped with resume_later%';" >/dev/null
	local before health_before
	before=$(ci_wait_ids)
	capture_health "${STORE:-sqlite}-close-before" health -json
	health_before=$(jq -c '[(.ciWaits // [])[].taskId]' "$WORK/captures/${STORE:-sqlite}-close-before.stdout")
	api POST "/api/tasks/$id/close" '{}' >/dev/null
	local after health_after
	after=$(ci_wait_ids)
	capture_health "${STORE:-sqlite}-close-after" health
	capture_health "${STORE:-sqlite}-close-after-json" health -json
	health_after=$(jq -c '[(.ciWaits // [])[].taskId]' "$WORK/captures/${STORE:-sqlite}-close-after-json.stdout")
	api POST "/api/tasks/$id/reopen" '{}' >/dev/null
	local reopened_state reopened_waits
	reopened_state=$(state_of "$id")
	reopened_waits=$(ci_wait_ids)
	# CI reports on the branch: the restored hold wakes on it.
	local branch sha
	branch=$(api GET "/api/tasks/$id" | jq -r '.branch')
	sha=$(git --git-dir="$WORK/widgets.git" rev-parse "refs/heads/$branch")
	finish_with "$id" "CI is green"
	curl -fsS -X POST "http://$GHSIM/_sim/seed-actions" -H "Content-Type: application/json" -d "{\"runs\":[
		{\"id\":5001,\"name\":\"CI\",\"path\":\".github/workflows/ci.yml\",\"headBranch\":\"main\",\"headSha\":\"0000000000000000000000000000000000000001\",\"event\":\"push\",\"status\":\"completed\",\"conclusion\":\"success\"},
		{\"id\":$((6000 + id)),\"name\":\"CI\",\"path\":\".github/workflows/ci.yml\",\"headBranch\":\"$branch\",\"headSha\":\"$sha\",\"event\":\"pull_request\",\"status\":\"completed\",\"conclusion\":\"success\",\"createdAt\":\"$(date -u +%FT%TZ)\",\"updatedAt\":\"$(date -u +%FT%TZ)\"}]}" >/dev/null
	local took=""
	took=$(left_deferred_within "$id" 30) || took="never"
	snapshot "$id" close_reopen
	result "close_reopen: task $id ciWaits before close=$before health=$health_before; after close=$after health=$health_after; reopened state=$reopened_state ciWaits=$reopened_waits; woke on CI after $took (log: $(grep -c "task $id has the CI verdict it waited for" "$WORK/daemon.log"))"
}

# --- 3: a resting console session still wakes on its first reply --------
scenario_console() {
	local id
	cat >"$WORK/action-default.json" <<'EOF'
[{"kind": "resume_later", "mcp_args": {"reason": "resting until the operator replies", "when": {"message": true}}}]
EOF
	id=$(api POST "/api/tasks" '{"title":"Walk console session","noRepo":true,"interactive":true,"approved":true}' | jq -r '.id')
	if ! wait_state "$id" deferred 60 >/dev/null; then
		result "console: FAIL task $id never rested ($(state_of "$id"))"
		rm -f "$WORK/action-default.json"
		return
	fi
	rm -f "$WORK/action-default.json"
	finish_with "$id" "thanks for the reply"
	api POST "/api/tasks/$id/comments" '{"body":"Here is my first reply."}' >/dev/null
	local took
	if took=$(left_deferred_within "$id" 20); then
		result "console: PASS task $id (interactive=$(api GET "/api/tasks/$id" | jq -r .interactive)) woke $took after its first reply"
	else
		result "console: FAIL task $id still $(state_of "$id") 20s after its first reply"
	fi
	snapshot "$id" console
}

main() {
	local scenarios=("$@")
	if [[ ${#scenarios[@]} -eq 0 ]]; then
		scenarios=(c c_person e e_person backstop_never backstop_unreadable backstop_nomode backstop_then_sendback close_reopen console)
	fi
	if [[ ${REUSE:-0} != 1 ]]; then
		init_rl_env "$WORK" >/dev/null
		start_ghsim "$WORK"
		# shellcheck disable=SC2086
		start_daemon "$WORK" ${DAEMON_ARGS:-} || exit 1
	else
		export WORK_DIR=$WORK DB="$WORK/data/store/grain.db" CODE
		CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	fi
	echo "daemon $DAEMON store ${STORE:-sqlite} binary $GRAIN ($("$GRAIN" version 2>/dev/null | head -1))"
	for s in "${scenarios[@]}"; do
		"scenario_$s"
	done
}

main "$@"
