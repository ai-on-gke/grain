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

# Full live walkthrough for docs/test-plans/2026-11-16.md §5:
# Runs against a real grain daemon (-sandbox-port-base=0) on STORE=sqlite
# or STORE=postgres, walking items 1-4 and capturing grain usage --json,
# GET /api/usage, SQL tables, fallback notices, and Playwright outputs.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
source "$HERE/env.sh"

STORE=${STORE:-sqlite}
WORK_DIR=${WORK_DIR:-/tmp/walk-weekly-${STORE}}
RUN_PW=${RUN_PW:-1}
SHOTS_DIR=${SHOTS_DIR:-}

wait_chat_turn() {
	local tid=$1 want_runs=$2 timeout=${3:-90}
	local i st nruns
	for i in $(seq "$timeout"); do
		local j
		j=$(api GET "/api/tasks/$tid")
		st=$(jq -r '.task.state // .state // ""' <<<"$j")
		nruns=$(jq -r '(.attempts // []) | length' <<<"$j")
		if [[ "$st" == "awaiting_reply" && "$nruns" -ge "$want_runs" ]]; then
			return 0
		fi
		sleep 0.5
	done
	echo "timeout waiting for chat task $tid runs>=$want_runs state=awaiting_reply (last state=$st runs=$nruns)" >&2
	return 1
}

init_env "$WORK_DIR"
trap 'stop_env "$WORK_DIR"' EXIT
start_ghsim "$WORK_DIR"
start_daemon "$WORK_DIR"

# Configure fallback chain: claude-opus-4-6 -> gemini-3.1-pro.
api PUT /api/settings '{"defaultModel":"claude-opus-4-6, gemini-3.1-pro"}' >/dev/null
sleep 0.5

echo "=== [Step 1a] Seed claude-opus-4-6 runs (2 tasks + 4 interactive chats) and age >5h ==="
for i in 1 2; do
	tid=$(api POST /api/tasks "{\"title\":\"Seed weekly usage $i\",\"description\":\"seed run $i\",\"repo\":\"acme/widgets\",\"approved\":true}" | jq -r '.id // .task.id')
	wait_task_state "$tid" closed 60
done

# Seed 4 interactive chat tasks on claude so each holds a claude SuspendedSession
# ready to test live fallback notices across timezones once claude hits its weekly limit.
cat >"$WORK_DIR/action-claude-default.json" <<'JSON'
{"kind":"comment_on_issue","comment":"Turn 1 handled on claude-opus-4-6.","usage":{"input_tokens":200000,"output_tokens":20000,"cache_read_input_tokens":50000,"cache_creation_input_tokens":0}}
JSON

t_chat_la=$(curl -fsS -X POST \
	-H "Authorization: Bearer $CODE" \
	-H "Content-Type: application/json" \
	-H "X-Grain-Time-Zone: America/Los_Angeles" \
	-d '{"title":"Chat LA timezone check","description":"Turn 1 on claude","repo":"acme/widgets","interactive":true,"approved":true}' \
	"$DAEMON/api/tasks" | jq -r '.id // .task.id')
wait_chat_turn "$t_chat_la" 1 60

t_chat_in=$(curl -fsS -X POST \
	-H "Authorization: Bearer $CODE" \
	-H "Content-Type: application/json" \
	-H "X-Grain-Time-Zone: Asia/Kolkata" \
	-d '{"title":"Chat Kolkata timezone check","description":"Turn 1 on claude","repo":"acme/widgets","interactive":true,"approved":true}' \
	"$DAEMON/api/tasks" | jq -r '.id // .task.id')
wait_chat_turn "$t_chat_in" 1 60

t_chat_dep=$(api POST /api/tasks '{"title":"Chat deployment TZ fallback","description":"Turn 1 on claude, no TZ header","repo":"acme/widgets","interactive":true,"approved":true}' | jq -r '.id // .task.id')
wait_chat_turn "$t_chat_dep" 1 60

t_chat_utc=$(api POST /api/tasks '{"title":"Chat UTC fallback","description":"Turn 1 on claude, no TZ header","repo":"acme/widgets","interactive":true,"approved":true}' | jq -r '.id // .task.id')
wait_chat_turn "$t_chat_utc" 1 60

# Backdate all seeded runs to 6h ago so the 5h interval window has 0 runs while the 7d week holds billable tokens.
if [[ "$STORE" == sqlite ]]; then
	six_h_ago=$(date -u -d '360 minutes ago' '+%Y-%m-%d %H:%M:%S +0000 UTC')
	six_h_start=$(date -u -d '370 minutes ago' '+%Y-%m-%d %H:%M:%S +0000 UTC')
	store_exec "UPDATE task_run_usage SET started_at='$six_h_start', finished_at='$six_h_ago';"
else
	store_exec "UPDATE task_run_usage SET started_at = NOW() - INTERVAL '370 minutes', finished_at = NOW() - INTERVAL '360 minutes';"
fi
capture_usage "step1a-aged-before-weekly-pause"

echo "=== [Step 1b] Trigger live weekly limit refusal (reset +72h) and observe fallback ==="
cat >"$WORK_DIR/action-claude-default.json" <<'JSON'
{"kind":"weekly_limit","reset_hours":72}
JSON
cat >"$WORK_DIR/action-agy-default.json" <<'JSON'
{"kind":"nothing_needed","reason":"Fallback run completed on gemini-3.1-pro.","usage":{"input_tokens":120000,"output_tokens":15000,"cache_read_tokens":30000}}
JSON

t_weekly=$(curl -fsS -X POST \
	-H "Authorization: Bearer $CODE" \
	-H "Content-Type: application/json" \
	-H "X-Grain-Time-Zone: America/Los_Angeles" \
	-d '{"title":"Trigger weekly limit and fall back","description":"Should hit weekly limit on Claude then fall back to Gemini","repo":"acme/widgets","approved":true}' \
	"$DAEMON/api/tasks" | jq -r '.id // .task.id')
wait_task_state "$t_weekly" closed 90
# Give async LearnFromPause goroutine a moment to commit.
sleep 1.5

# File a pinned claude-opus-4-6 task and confirm it stays queued (blocked while weekly-paused).
t_pinned=$(api POST /api/tasks '{"title":"Pinned to weekly-paused Claude","description":"Must remain queued while weekly paused","repo":"acme/widgets","model":"claude-opus-4-6","approved":true}' | jq -r '.id // .task.id')
sleep 3

capture_usage "step1b-weekly-paused"
api GET "/api/tasks/$t_weekly" >"$WORK_DIR/captures/step1b-task-weekly-detail.json"
api GET "/api/tasks/$t_pinned" >"$WORK_DIR/captures/step1b-task-pinned-detail.json"
store_exec "SELECT connection_id, model, tokens, window_ms, observed_at, reset_at FROM usage_capacity ORDER BY 1,2;" >"$WORK_DIR/captures/step1b-usage-capacity.txt"
store_exec "SELECT scope_key, connection_id, model, until_at, reason, escalated FROM usage_limit_pause ORDER BY 1;" >"$WORK_DIR/captures/step1b-usage-limit-pause.txt"

echo "=== [Step 1c] Item 4: Interactive fallback notices across timezones + Playwright checks ==="
# Switch fallback agent to comment_on_issue so interactive Turn 2 rests in awaiting_reply with fallback notice.
cat >"$WORK_DIR/action-agy-default.json" <<'JSON'
{"kind":"comment_on_issue","comment":"Turn 2 handled on gemini-3.1-pro fallback.","usage":{"input_tokens":120000,"output_tokens":15000,"cache_read_tokens":30000}}
JSON

# Trigger Turn 2 fallback on LA and Kolkata interactive chats.
curl -fsS -X POST \
	-H "Authorization: Bearer $CODE" \
	-H "Content-Type: application/json" \
	-H "X-Grain-Time-Zone: America/Los_Angeles" \
	-d '{"body":"Follow-up from Los Angeles"}' \
	"$DAEMON/api/tasks/$t_chat_la/comments" >/dev/null
wait_chat_turn "$t_chat_la" 2 90
api GET "/api/tasks/$t_chat_la" >"$WORK_DIR/captures/step4-chat-la.json"

curl -fsS -X POST \
	-H "Authorization: Bearer $CODE" \
	-H "Content-Type: application/json" \
	-H "X-Grain-Time-Zone: Asia/Kolkata" \
	-d '{"body":"Follow-up from Kolkata"}' \
	"$DAEMON/api/tasks/$t_chat_in/comments" >/dev/null
wait_chat_turn "$t_chat_in" 2 90
api GET "/api/tasks/$t_chat_in" >"$WORK_DIR/captures/step4-chat-kolkata.json"

# Deployment TimeZone fallback (America/New_York) when task has no timeZone.
api PUT /api/settings '{"timeZone":"America/New_York"}' >/dev/null
sleep 0.5
api POST "/api/tasks/$t_chat_dep/comments" '{"body":"Follow-up with no TZ header (expect deployment America/New_York)"}' >/dev/null
wait_chat_turn "$t_chat_dep" 2 90
api GET "/api/tasks/$t_chat_dep" >"$WORK_DIR/captures/step4-chat-dep-tz.json"

# UTC fallback when neither task nor deployment has a TimeZone.
api PUT /api/settings '{"timeZone":""}' >/dev/null
sleep 0.5
api POST "/api/tasks/$t_chat_utc/comments" '{"body":"Follow-up with no TZ header and empty deployment TZ (expect UTC)"}' >/dev/null
wait_chat_turn "$t_chat_utc" 2 90
api GET "/api/tasks/$t_chat_utc" >"$WORK_DIR/captures/step4-chat-utc.json"

if [[ "$RUN_PW" == 1 ]]; then
	# Ensure fallback agent closes non-interactive tasks filed by Playwright.
	cat >"$WORK_DIR/action-agy-default.json" <<'JSON'
{"kind":"nothing_needed","reason":"Playwright task handled on gemini-3.1-pro fallback.","usage":{"input_tokens":80000,"output_tokens":10000,"cache_read_tokens":10000}}
JSON
	(cd "$ROOT/ui-ng" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node "$HERE/pw_walkthrough.mjs" "$DAEMON" "$CODE" "$WORK_DIR/captures/step4-playwright.json" "$SHOTS_DIR")
	for tid in $(jq -r '.filed[].taskId | select(. != "")' "$WORK_DIR/captures/step4-playwright.json"); do
		wait_task_state "$tid" closed 90 || true
		api GET "/api/tasks/$tid" >"$WORK_DIR/captures/step4-pw-task-${tid}.json"
	done
fi
store_exec "SELECT id, title, time_zone FROM task ORDER BY id;" >"$WORK_DIR/captures/step4-task-timezones.txt"

echo "=== [Step 3] Persistence across restart and grain/config sync ==="
capture_usage "step3a-before-restart"
stop_daemon "$WORK_DIR"
start_daemon "$WORK_DIR"
sleep 1.5
capture_usage "step3b-after-restart"
api GET "/api/tasks/$t_pinned" >"$WORK_DIR/captures/step3b-task-pinned-after-restart.json"
store_exec "SELECT connection_id, model, tokens, window_ms, observed_at, reset_at FROM usage_capacity ORDER BY 1,2;" >"$WORK_DIR/captures/step3b-usage-capacity.txt"
store_exec "SELECT scope_key, connection_id, model, until_at, reason, escalated FROM usage_limit_pause ORDER BY 1;" >"$WORK_DIR/captures/step3b-usage-limit-pause.txt"

# Test grain/config sync & grain state check while #weekly capacity row exists.
if [[ "$STORE" == sqlite ]]; then
	"$GRAIN" state -data-dir "$WORK_DIR/data" sync >"$WORK_DIR/captures/step3c-state-sync.txt" 2>&1
else
	GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN="$PG_DSN" "$GRAIN" state -data-dir "$WORK_DIR/data" sync >"$WORK_DIR/captures/step3c-state-sync.txt" 2>&1
fi
"$GRAIN" state check "$WORK_DIR/data/state-repo" >"$WORK_DIR/captures/step3c-state-check.txt" 2>&1
store_exec "SELECT connection_id, model, tokens, window_ms, observed_at, reset_at FROM usage_capacity ORDER BY 1,2;" >"$WORK_DIR/captures/step3c-usage-capacity-after-sync.txt"

# Close pinned task and chats so they do not interfere with Step 2.
for tid in "$t_pinned" "$t_chat_la" "$t_chat_in" "$t_chat_dep" "$t_chat_utc"; do
	api POST "/api/tasks/$tid/close" '{"reason":"walkthrough moving to step 2"}' >/dev/null || true
done

echo "=== [Step 2a] Lift weekly pause, simulate late-in-week interval pause in the same week ==="
api DELETE "/api/pause" >/dev/null
sleep 0.8
# Set weekly capacity reset_at to +12h (late in the week) and make claude succeed once, then hit 5-hour interval limit!
if [[ "$STORE" == sqlite ]]; then
	twelve_h=$(date -u -d '12 hours' '+%Y-%m-%d %H:%M:%S +0000 UTC')
	store_exec "UPDATE usage_capacity SET reset_at='$twelve_h' WHERE model LIKE '%#weekly';"
else
	store_exec "UPDATE usage_capacity SET reset_at = NOW() + INTERVAL '12 hours' WHERE model LIKE '%#weekly';"
fi

cat >"$WORK_DIR/action-claude-default.json" <<'JSON'
[
  {"kind":"nothing_needed","reason":"mid-week work before interval limit","usage":{"input_tokens":350000,"output_tokens":50000,"cache_read_input_tokens":100000,"cache_creation_input_tokens":0}},
  {"kind":"interval_limit","reset_hours":2.25}
]
JSON
cat >"$WORK_DIR/action-agy-default.json" <<'JSON'
{"kind":"nothing_needed","reason":"Interval fallback run completed on gemini-3.1-pro.","usage":{"input_tokens":120000,"output_tokens":15000,"cache_read_tokens":30000}}
JSON

t_mid=$(api POST /api/tasks '{"title":"Mid-week run before interval limit","description":"Spends tokens in current 5h window","repo":"acme/widgets","approved":true}' | jq -r '.id // .task.id')
wait_task_state "$t_mid" closed 60

t_int=$(api POST /api/tasks '{"title":"Trigger interval limit in same week","description":"Hits 5h limit while #weekly exists","repo":"acme/widgets","approved":true}' | jq -r '.id // .task.id')
wait_task_state "$t_int" closed 90
sleep 1.5

capture_usage "step2a-interval-paused-same-week"
store_exec "SELECT connection_id, model, tokens, window_ms, observed_at, reset_at FROM usage_capacity ORDER BY 1,2;" >"$WORK_DIR/captures/step2a-usage-capacity.txt"
store_exec "SELECT scope_key, connection_id, model, until_at, reason, escalated FROM usage_limit_pause ORDER BY 1;" >"$WORK_DIR/captures/step2a-usage-limit-pause.txt"

echo "=== [Step 2b] Auto-mode worker cap late in the week ==="
api DELETE "/api/pause" >/dev/null
sleep 0.5
# Backdate the mid-week run's duration to 10m (so tokensPerWorkerHour is finite and well-defined)
# and set weekly limit slightly above current weeklyUsed with 12h left in the week so sustainable workers < max_workers (4).
if [[ "$STORE" == sqlite ]]; then
	m10=$(date -u -d '10 minutes ago' '+%Y-%m-%d %H:%M:%S +0000 UTC')
	now_s=$(date -u '+%Y-%m-%d %H:%M:%S +0000 UTC')
	store_exec "UPDATE task_run_usage SET started_at='$m10', finished_at='$now_s' WHERE run_id='${t_mid}-1';"
else
	store_exec "UPDATE task_run_usage SET started_at = NOW() - INTERVAL '10 minutes', finished_at = NOW() WHERE run_id='${t_mid}-1';"
fi
# Raise weekly capacity to ~200k above current weeklyUsed (~2.33M -> ~2.53M so ~200k remaining over 12h!)
# and set auto mode + budget on gemini-fallback so deployment sustainableWorkers is bounded.
store_exec "UPDATE usage_capacity SET tokens = 2530000 WHERE model LIKE '%#weekly';"
grain_cli usage -mode auto -min-workers 1 -budget "gemini-fallback/gemini-3.1-pro=2000000/5h" >"$WORK_DIR/captures/step2b-set-auto.txt"
sleep 0.5
capture_usage "step2b-auto-cap-late-week"

echo "=== Walkthrough complete for STORE=$STORE (artifacts in $WORK_DIR/captures) ==="
