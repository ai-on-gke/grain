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

# walk_items_1_2_3.sh exercises §4 items 1, 2, and 3 against a real
# `grain daemon -sandbox-port-base=0 -reconcile 1s` with `githubsim_server`
# and host-sandboxed grain CI.
set -euo pipefail

GRAIN_BIN=${GRAIN_BIN:-/home/debian/bin/grain}
SIM_BIN=${SIM_BIN:-/home/debian/bin/githubsim_server}
WORK_ROOT=${WORK_ROOT:-/tmp/walk-sched-grain-ci}
SIM_PORT=${SIM_PORT:-4250}
UI_PORT=${UI_PORT:-4251}
SIM_URL="http://127.0.0.1:${SIM_PORT}"
API_URL="http://127.0.0.1:${UI_PORT}"

cleanup() {
  set +e
  if [ -f "$WORK_ROOT/daemon.pid" ]; then
    kill "$(cat "$WORK_ROOT/daemon.pid")" 2>/dev/null || true
  fi
  if [ -f "$WORK_ROOT/sim.pid" ]; then
    kill "$(cat "$WORK_ROOT/sim.pid")" 2>/dev/null || true
  fi
}
trap cleanup EXIT

rm -rf "$WORK_ROOT"
mkdir -p "$WORK_ROOT/repos" "$WORK_ROOT/data" "$WORK_ROOT/sb" "$WORK_ROOT/clones" "$WORK_ROOT/out"

echo "=== Starting githubsim_server on 127.0.0.1:${SIM_PORT} ==="
setsid "$SIM_BIN" \
  -addr "127.0.0.1:${SIM_PORT}" \
  -repos-dir "$WORK_ROOT/repos" \
  -repos "acme/widgets,acme/other,ai-on-gke/grain" \
  -token "sim-token" \
  >"$WORK_ROOT/sim.log" 2>&1 </dev/null &
echo $! >"$WORK_ROOT/sim.pid"

for _ in $(seq 30); do
  if curl -fsS "$SIM_URL/_sim/state" >/dev/null 2>&1; then break; fi
  sleep 0.2
done
curl -fsS "$SIM_URL/_sim/state" >/dev/null

# Helper to initialize and push files to a simulated repo's main branch.
init_repo() {
  local repo=$1
  local dir="$WORK_ROOT/clones/${repo//\//_}"
  rm -rf "$dir"
  mkdir -p "$dir"
  git -C "$dir" init -q -b main
  git -C "$dir" config user.name "Alice Example"
  git -C "$dir" config user.email "alice@example.com"
  git -C "$dir" remote add origin "http://sim-token:@127.0.0.1:${SIM_PORT}/${repo}.git"
}

init_repo "acme/widgets"
echo "pass" >"$WORK_ROOT/clones/acme_widgets/status.txt"
mkdir -p "$WORK_ROOT/clones/acme_widgets/.grain/workflows" "$WORK_ROOT/clones/acme_widgets/.grain/schedules" "$WORK_ROOT/clones/acme_widgets/.grain/templates"
cat >"$WORK_ROOT/clones/acme_widgets/.grain/templates/fix-up.yml" <<'EOF'
title: Fix widgets issue
body: |
  Fix the widget issue and push a branch, or call nothing_needed if clean.
nothing-needed: close
EOF
cat >"$WORK_ROOT/clones/acme_widgets/.grain/workflows/check.yml" <<'EOF'
name: check
on:
  push:
  pull_request:
  schedule:
    - cron: "11 2 * * *"
jobs:
  check:
    steps:
      - name: verify status
        run: |
          echo "EVENT=${GRAIN_CI_EVENT:-unset}"
          if [ -f slow.txt ]; then
            sleep "$(cat slow.txt)"
          fi
          grep -qx pass status.txt
EOF
cat >"$WORK_ROOT/clones/acme_widgets/.grain/schedules/check.yml" <<'EOF'
workflow: check
grain-ci: true
every:
  minutes: 10
EOF
git -C "$WORK_ROOT/clones/acme_widgets" add -A
git -C "$WORK_ROOT/clones/acme_widgets" commit -q -m "Initial acme/widgets check workflow and grain-ci schedule"
git -C "$WORK_ROOT/clones/acme_widgets" push -q -u origin main

init_repo "acme/other"
echo "ok" >"$WORK_ROOT/clones/acme_other/README.md"
git -C "$WORK_ROOT/clones/acme_other" add -A
git -C "$WORK_ROOT/clones/acme_other" commit -q -m "Initial acme/other"
git -C "$WORK_ROOT/clones/acme_other" push -q -u origin main

init_repo "ai-on-gke/grain"
echo "0" >"$WORK_ROOT/clones/ai-on-gke_grain/lag.txt"
git -C "$WORK_ROOT/clones/ai-on-gke_grain" add -A
git -C "$WORK_ROOT/clones/ai-on-gke_grain" commit -q -m "Initial ai-on-gke/grain fixture"
git -C "$WORK_ROOT/clones/ai-on-gke_grain" push -q -u origin main

# Fake agy binary so the daemon starts cleanly without external model credentials
# and never auto-completes review tasks unless asked.
cat >"$WORK_ROOT/fake-agy.sh" <<'EOF'
#!/usr/bin/env bash
for arg in "$@"; do
  case "$arg" in --help) exit 0;; --version) echo 'fake agy'; exit 0;; models) exit 0;; esac
done
exit 0
EOF
chmod +x "$WORK_ROOT/fake-agy.sh"

echo "=== Starting real grain daemon on 127.0.0.1:${UI_PORT} ==="
setsid "$GRAIN_BIN" daemon \
  -data-dir "$WORK_ROOT/data" \
  -ui-addr "127.0.0.1:${UI_PORT}" \
  -sandbox-port-base=0 \
  -host-sandboxes \
  -sandbox-dir "$WORK_ROOT/sb" \
  -ci \
  -reconcile \
  -poll-interval 1s \
  -github-host "127.0.0.1:${SIM_PORT}" \
  -github-insecure-http \
  -agy-path "$WORK_ROOT/fake-agy.sh" \
  >"$WORK_ROOT/daemon.log" 2>&1 </dev/null &
echo $! >"$WORK_ROOT/daemon.pid"

for _ in $(seq 60); do
  kill -0 "$(cat "$WORK_ROOT/daemon.pid")" 2>/dev/null || { echo "daemon exited early:"; tail -n 40 "$WORK_ROOT/daemon.log"; exit 1; }
  if [ -s "$WORK_ROOT/data/local-api-token" ]; then
    TOK=$(cat "$WORK_ROOT/data/local-api-token")
    if curl -fsS -H "Authorization: Bearer $TOK" "$API_URL/api/config" >/dev/null 2>&1; then
      break
    fi
  fi
  sleep 0.5
done
TOK=$(cat "$WORK_ROOT/data/local-api-token")

api() {
  local method=$1 path=$2 body=${3:-}
  if [ -n "$body" ]; then
    curl -sS -X "$method" -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" --data "$body" "$API_URL$path"
  else
    curl -sS -X "$method" -H "Authorization: Bearer $TOK" "$API_URL$path"
  fi
}

api_code() {
  local method=$1 path=$2 body=${3:-}
  if [ -n "$body" ]; then
    curl -sS -o "$WORK_ROOT/last_resp.json" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" --data "$body" "$API_URL$path"
  else
    curl -sS -o "$WORK_ROOT/last_resp.json" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $TOK" "$API_URL$path"
  fi
}

# No agent model backend is configured, so filed review/template tasks stay queued for inspection while schedules fire normally.
api PUT /api/github-tokens/default '{"value":"sim-token"}' >/dev/null

for r in acme/widgets acme/other ai-on-gke/grain; do
  api POST /api/repos "{\"repo\":\"$r\"}" >/dev/null
  owner=${r%/*}; name=${r#*/}
  api PUT "/api/repos/$owner/$name/ci-mode" '{"ciMode":"github"}' >/dev/null
  api PUT "/api/repos/$owner/$name/declarations" '{"declarations":true}' >/dev/null
done

echo "=== Waiting for acme/widgets schedule declaration sync ==="
SCHED_ID="repo:schedule:acme:widgets:check"
for _ in $(seq 30); do
  if api GET /api/schedules | jq -e --arg id "$SCHED_ID" '.[] | select(.id == $id)' >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
api GET /api/schedules | jq --arg id "$SCHED_ID" '.[] | select(.id == $id)' >"$WORK_ROOT/out/item1_synced_schedule.json"
cat "$WORK_ROOT/out/item1_synced_schedule.json"

echo "=== ITEM 1a/1b/1c (green): Waiting for real cadence firing of grain-ci: true schedule ==="
curl -fsS -X POST "$SIM_URL/_sim/reset-log" >/dev/null
# Move next_run_at to 2s ago so the real DueSchedules cadence loop fires it on the next 1s reconcile tick!
# (Also verify natural cadence advance after firing.)
DB="$WORK_ROOT/data/store/grain.db"
sqlite3 "$DB" "UPDATE schedule SET next_run_at = strftime('%Y-%m-%d %H:%M:%S +0000 UTC', 'now', '-2 seconds') WHERE id = '$SCHED_ID';"

PASS_RUN_ID=""
for _ in $(seq 45); do
  PASS_RUN_ID=$(sqlite3 "$DB" "SELECT id FROM ci_run WHERE repo='acme/widgets' AND conclusion='success' LIMIT 1;")
  if [ -n "$PASS_RUN_ID" ]; then break; fi
  sleep 1
done
echo "Green scheduled run ID: $PASS_RUN_ID"
[ -n "$PASS_RUN_ID" ] || { echo "FAIL: green scheduled run did not finish"; tail -n 50 "$WORK_ROOT/daemon.log"; exit 1; }

# Wait 2 reconcile ticks for settleScheduledWorkflowRun.
sleep 3
sqlite3 -header -column "$DB" "SELECT id, repo, ref, event, requested_by, conclusion, cancel_reason FROM ci_run WHERE id='$PASS_RUN_ID';" | tee "$WORK_ROOT/out/item1_green_cirun.txt"
sqlite3 -header -column "$DB" "SELECT schedule_id, run_id, conclusion, review_task_id, review_not_needed FROM scheduled_workflow_run WHERE schedule_id='$SCHED_ID';" | tee "$WORK_ROOT/out/item1_green_swr.txt"
curl -fsS "$SIM_URL/_sim/state" >"$WORK_ROOT/out/item1_green_sim_state.json"
echo "Simulator checkWrites / statusWrites / checkRuns after green run:"
jq '{statusWrites, checkWrites, widgetsChecks: .repos["acme/widgets"].checkRuns}' "$WORK_ROOT/out/item1_green_sim_state.json"
echo "Tasks filed on acme/widgets after green run:"
api GET /api/tasks | jq '[.[] | select(.repo == "acme/widgets")] | length' | tee "$WORK_ROOT/out/item1_green_tasks_count.txt"

echo "=== ITEM 1d: Confirm pushes to main and PRs run NO grain CI on ci_mode: github ==="
CI_COUNT_BEFORE=$(sqlite3 "$DB" "SELECT COUNT(*) FROM ci_run WHERE repo='acme/widgets';")
echo "fail" >"$WORK_ROOT/clones/acme_widgets/status.txt"
git -C "$WORK_ROOT/clones/acme_widgets" commit -q -am "Break status.txt on main"
git -C "$WORK_ROOT/clones/acme_widgets" push -q origin main

git -C "$WORK_ROOT/clones/acme_widgets" checkout -q -b feat/widget-pr
echo "pr-change" >"$WORK_ROOT/clones/acme_widgets/pr.txt"
git -C "$WORK_ROOT/clones/acme_widgets" add pr.txt
git -C "$WORK_ROOT/clones/acme_widgets" commit -q -m "Open PR branch"
git -C "$WORK_ROOT/clones/acme_widgets" push -q -u origin feat/widget-pr
git -C "$WORK_ROOT/clones/acme_widgets" checkout -q main
curl -fsS -X POST "$SIM_URL/repos/acme/widgets/pulls" \
  -H "Content-Type: application/json" \
  --data '{"title":"Widget PR","head":"feat/widget-pr","base":"main","body":"test"}' >"$WORK_ROOT/out/item1_pr.json"

# Also file an approved task with that PR branch to ensure reconcileTaskCI / reconcileBaseBranchCI run!
PR_TASK_JSON=$(api POST /api/tasks '{"title":"Widget PR task","description":"test","repo":"acme/widgets","branch":"feat/widget-pr","approved":true}')
PR_TASK_ID=$(echo "$PR_TASK_JSON" | jq -r '.id')
api POST /api/repos/acme/widgets/lifecycle/refresh '{}' >/dev/null || true
sqlite3 "$DB" "DELETE FROM repo_declaration_sync;"
sleep 5
CI_COUNT_AFTER=$(sqlite3 "$DB" "SELECT COUNT(*) FROM ci_run WHERE repo='acme/widgets';")
echo "ci_run count before push+PR: $CI_COUNT_BEFORE, after push+PR: $CI_COUNT_AFTER" | tee "$WORK_ROOT/out/item1_push_pr_cicheck.txt"
api POST "/api/tasks/$PR_TASK_ID/close" '{"reason":"done"}' >/dev/null || api PATCH "/api/tasks/$PR_TASK_ID" '{"closed":true}' >/dev/null || true

echo "=== ITEM 1c (red): Failing scheduled run files a review task and posts no status/check ==="
curl -fsS -X POST "$SIM_URL/_sim/reset-log" >/dev/null
sqlite3 "$DB" "UPDATE schedule SET next_run_at = strftime('%Y-%m-%d %H:%M:%S +0000 UTC', 'now', '-2 seconds') WHERE id = '$SCHED_ID';"
FAIL_RUN_ID=""
for _ in $(seq 45); do
  FAIL_RUN_ID=$(sqlite3 "$DB" "SELECT id FROM ci_run WHERE repo='acme/widgets' AND conclusion='failure' LIMIT 1;")
  if [ -n "$FAIL_RUN_ID" ]; then break; fi
  sleep 1
done
echo "Red scheduled run ID: $FAIL_RUN_ID"
sleep 3
sqlite3 -header -column "$DB" "SELECT schedule_id, run_id, conclusion, review_task_id, review_not_needed FROM scheduled_workflow_run WHERE schedule_id='$SCHED_ID';" | tee "$WORK_ROOT/out/item1_red_swr.txt"
REVIEW_TASK_ID=$(sqlite3 "$DB" "SELECT review_task_id FROM scheduled_workflow_run WHERE schedule_id='$SCHED_ID';")
echo "Filed review task ID: $REVIEW_TASK_ID"
curl -fsS "$SIM_URL/_sim/state" | jq '{statusWrites, checkWrites, widgetsChecks: .repos["acme/widgets"].checkRuns}' | tee "$WORK_ROOT/out/item1_red_sim_state.json"
# Close the review task via the real API so overlap: skip doesn't hold future firings.
api POST "/api/tasks/$REVIEW_TASK_ID/close" '{"reason":"reviewed in walkthrough"}' >/dev/null

echo "=== ITEM 1e: Remove grain-ci: true -> next firing dropped & recorded firing deleted ==="
echo "pass" >"$WORK_ROOT/clones/acme_widgets/status.txt"
cat >"$WORK_ROOT/clones/acme_widgets/.grain/schedules/check.yml" <<'EOF'
workflow: check
every:
  minutes: 10
EOF
git -C "$WORK_ROOT/clones/acme_widgets" commit -q -am "Remove grain-ci: true from check schedule"
git -C "$WORK_ROOT/clones/acme_widgets" push -q origin main
api POST /api/repos/acme/widgets/lifecycle/refresh '{}' >/dev/null || true
sqlite3 "$DB" "DELETE FROM repo_declaration_sync;"

for _ in $(seq 30); do
  GCI=$(sqlite3 "$DB" "SELECT grain_ci FROM schedule WHERE id='$SCHED_ID';")
  if [ "$GCI" = "0" ]; then break; fi
  sleep 1
done
echo "Synced schedule.grain_ci after removal: $(sqlite3 "$DB" "SELECT grain_ci FROM schedule WHERE id='$SCHED_ID';")"
echo "Settled scheduled_workflow_run row immediately after removing grain-ci:"
sqlite3 -header -column "$DB" "SELECT schedule_id, run_id, conclusion, review_not_needed FROM scheduled_workflow_run WHERE schedule_id='$SCHED_ID';" | tee "$WORK_ROOT/out/item1e_settled_row_after_removal.txt"

# Also verify an unstarted recorded firing (e.g. recorded just before grain-ci turned off) IS deleted,
# and a due cadence firing is dropped with SuppressedByStandingWork ("this repository uses github CI mode").
CI_BEFORE_DROP=$(sqlite3 "$DB" "SELECT COUNT(*) FROM ci_run WHERE repo='acme/widgets';")
sqlite3 "$DB" "DELETE FROM scheduled_firing_skip WHERE schedule_id='$SCHED_ID';"
sqlite3 "$DB" "UPDATE schedule SET next_run_at = strftime('%Y-%m-%d %H:%M:%S +0000 UTC', 'now', '-2 seconds') WHERE id = '$SCHED_ID';"
sleep 3
CI_AFTER_DROP=$(sqlite3 "$DB" "SELECT COUNT(*) FROM ci_run WHERE repo='acme/widgets';")
echo "ci_run count before/after due tick without grain-ci: $CI_BEFORE_DROP -> $CI_AFTER_DROP" | tee "$WORK_ROOT/out/item1e_dropped_firing.txt"
sqlite3 -header -column "$DB" "SELECT schedule_id, reason, detail FROM scheduled_firing_skip WHERE schedule_id='$SCHED_ID';" | tee -a "$WORK_ROOT/out/item1e_dropped_firing.txt"

# Test RunScheduleNow error message when grain-ci is off!
RUN_NOW_CODE=$(api_code POST "/api/schedules/$SCHED_ID/run" '{}')
echo "POST /api/schedules/$SCHED_ID/run without grain-ci -> HTTP $RUN_NOW_CODE: $(cat "$WORK_ROOT/last_resp.json")" | tee "$WORK_ROOT/out/item1e_run_now_refusal.txt"

# Insert an unstarted scheduled_workflow_run row (simulating a firing recorded just before ci_mode/grain-ci flipped) and verify startScheduledWorkflowRun deletes it:
sqlite3 "$DB" "INSERT OR REPLACE INTO scheduled_workflow_run (schedule_id, owner, name, stem, branch, fired_at, attempts, review_not_needed) VALUES ('$SCHED_ID', 'acme', 'widgets', 'check', 'main', strftime('%Y-%m-%d %H:%M:%S +0000 UTC', 'now'), 0, 0);"
sleep 3
UNSTARTED_LEFT=$(sqlite3 "$DB" "SELECT COUNT(*) FROM scheduled_workflow_run WHERE schedule_id='$SCHED_ID';")
echo "Unstarted scheduled_workflow_run rows remaining after reconcile: $UNSTARTED_LEFT" | tee "$WORK_ROOT/out/item1e_deleted_recorded_firing.txt"

echo "=== ITEM 2: Confinement rules ==="
# Restore grain-ci: true and add slow.txt=12 so runs stay alive long enough for sweep tests!
echo "12" >"$WORK_ROOT/clones/acme_widgets/slow.txt"
cat >"$WORK_ROOT/clones/acme_widgets/.grain/schedules/check.yml" <<'EOF'
workflow: check
grain-ci: true
every:
  minutes: 10
EOF
git -C "$WORK_ROOT/clones/acme_widgets" add -A
git -C "$WORK_ROOT/clones/acme_widgets" commit -q -m "Restore grain-ci: true with 12s step for confinement checks"
git -C "$WORK_ROOT/clones/acme_widgets" push -q origin main
api POST /api/repos/acme/widgets/lifecycle/refresh '{}' >/dev/null || true
sqlite3 "$DB" "DELETE FROM repo_declaration_sync;"
for _ in $(seq 30); do
  if [ "$(sqlite3 "$DB" "SELECT grain_ci FROM schedule WHERE id='$SCHED_ID';")" = "1" ]; then break; fi
  sleep 1
done

echo "--- 2a: manual grain ci run-job / retrigger refused upfront (403) + in-flight push run cancelled by sweep ---"
# Create a task on acme/widgets holding grain#ci (pre-existing grant) and push main's tree to its assigned branch.
T2A_JSON=$(api POST /api/tasks '{"title":"Task 2a push run-job","description":"test","repo":"acme/widgets"}')
T2A_ID=$(echo "$T2A_JSON" | jq -r '.id')
T2A_BRANCH=$(echo "$T2A_JSON" | jq -r '.branch')
git -C "$WORK_ROOT/clones/acme_widgets" push -q origin "HEAD:refs/heads/$T2A_BRANCH"
sqlite3 "$DB" "INSERT OR IGNORE INTO task_grant (task_id, capability, via) VALUES ('$T2A_ID', 'https://grain.dev/capabilities/grain#ci={}', 'default');"

RUNJOB_CODE=$(api_code POST /api/ci/runs "{\"taskId\":\"$T2A_ID\",\"workflow\":\".grain/workflows/check.yml\",\"jobs\":[\"check\"]}")
echo "run-job HTTP $RUNJOB_CODE: $(cat "$WORK_ROOT/last_resp.json")" | tee "$WORK_ROOT/out/item2a_runjob.txt"
[ "$RUNJOB_CODE" = "403" ] || { echo "FAIL: expected run-job HTTP 403 on ci_mode: github, got $RUNJOB_CODE"; exit 1; }

RETRIG_CODE=$(api_code POST /api/ci/runs "{\"taskId\":\"$T2A_ID\",\"recheck\":true}")
echo "retrigger HTTP $RETRIG_CODE: $(cat "$WORK_ROOT/last_resp.json")" | tee "$WORK_ROOT/out/item2a_retrigger.txt"
[ "$RETRIG_CODE" = "403" ] || { echo "FAIL: expected retrigger HTTP 403 on ci_mode: github, got $RETRIG_CODE"; exit 1; }

# Also start an in-flight push-event run while briefly ci_mode: grain, flip back to ci_mode: github
# (even forging requested_by='workflow-schedule:repo:schedule:acme:widgets:check'), and verify reconcileCI cancels it!
api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"grain"}' >/dev/null
PUSH_SWEEP_CODE=$(api_code POST /api/ci/runs "{\"taskId\":\"$T2A_ID\",\"workflow\":\".grain/workflows/check.yml\",\"jobs\":[\"check\"]}")
PUSH_SWEEP_ID=$(jq -r '.id // empty' "$WORK_ROOT/last_resp.json")
[ "$PUSH_SWEEP_CODE" = "202" ] && [ -n "$PUSH_SWEEP_ID" ] || { echo "FAIL: could not start push run under ci_mode: grain ($PUSH_SWEEP_CODE)"; exit 1; }
sqlite3 "$DB" "UPDATE ci_run SET requested_by='workflow-schedule:$SCHED_ID' WHERE id='$PUSH_SWEEP_ID';"
api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"github"}' >/dev/null
for _ in $(seq 15); do
  CONC=$(sqlite3 "$DB" "SELECT conclusion FROM ci_run WHERE id='$PUSH_SWEEP_ID';")
  if [ -n "$CONC" ]; then break; fi
  sleep 1
done
sqlite3 -header -column "$DB" "SELECT id, event, requested_by, conclusion, cancelled_by, cancel_reason FROM ci_run WHERE id='$PUSH_SWEEP_ID';" | tee "$WORK_ROOT/out/item2a_push_sweep.txt"
[ "$(sqlite3 "$DB" "SELECT conclusion FROM ci_run WHERE id='$PUSH_SWEEP_ID';")" = "cancelled" ] || { echo "FAIL: push-event run was not cancelled by sweep"; exit 1; }

# Helper to start a live schedule-event run and test sweep under a mutated requested_by.
test_sweep_requester() {
  local label=$1 new_req=$2
  sqlite3 "$DB" "DELETE FROM scheduled_workflow_run WHERE schedule_id='$SCHED_ID';"
  api POST "/api/schedules/$SCHED_ID/run" '{}' >/dev/null
  local rid=""
  for _ in $(seq 20); do
    rid=$(sqlite3 "$DB" "SELECT run_id FROM scheduled_workflow_run WHERE schedule_id='$SCHED_ID' AND run_id != '';")
    if [ -n "$rid" ]; then break; fi
    sleep 0.5
  done
  echo "[$label] started live schedule run $rid; setting requested_by='$new_req'"
  # Clear scheduled_workflow_run so collectScheduledWorkflowRun doesn't race the reconcileCI sweep.
  sqlite3 "$DB" "DELETE FROM scheduled_workflow_run WHERE schedule_id='$SCHED_ID'; UPDATE ci_run SET requested_by='$new_req' WHERE id='$rid';"
  for _ in $(seq 15); do
    local conc
    conc=$(sqlite3 "$DB" "SELECT conclusion FROM ci_run WHERE id='$rid';")
    if [ -n "$conc" ]; then break; fi
    sleep 0.5
  done
  sqlite3 -header -column "$DB" "SELECT id, event, requested_by, conclusion, cancelled_by, cancel_reason FROM ci_run WHERE id='$rid';" | tee "$WORK_ROOT/out/item2_${label}.txt"
}

echo "--- 2b: bogus #infra-retry-x suffix cancelled ---"
test_sweep_requester "2b_bogus_retry" "workflow-schedule:${SCHED_ID}#infra-retry-x"

echo "--- 2c: cross-repo schedule in acme/other targeting acme/widgets ---"
# First check repodecl refusal of repo:/target: in .grain/schedules/
mkdir -p "$WORK_ROOT/clones/acme_other/.grain/workflows" "$WORK_ROOT/clones/acme_other/.grain/schedules"
cat >"$WORK_ROOT/clones/acme_other/.grain/workflows/check.yml" <<'EOF'
name: check
on:
  schedule:
    - cron: "11 2 * * *"
jobs:
  check:
    steps:
      - name: ok
        run: true
EOF
cat >"$WORK_ROOT/clones/acme_other/.grain/schedules/cross.yml" <<'EOF'
workflow: check
grain-ci: true
target: acme/widgets
every:
  minutes: 5
EOF
set +e
"$GRAIN_BIN" declarations check "$WORK_ROOT/clones/acme_other" >"$WORK_ROOT/out/item2c_decl_check.txt" 2>&1
DECL_RC=$?
set -e
echo "grain declarations check on target: acme/widgets in acme/other -> exit=$DECL_RC: $(cat "$WORK_ROOT/out/item2c_decl_check.txt")"

# Now push a valid grain-ci: true schedule in acme/other and verify its requester cannot keep a run alive on acme/widgets!
rm -f "$WORK_ROOT/clones/acme_other/.grain/schedules/cross.yml"
cat >"$WORK_ROOT/clones/acme_other/.grain/schedules/check.yml" <<'EOF'
workflow: check
grain-ci: true
every:
  hours: 6
EOF
git -C "$WORK_ROOT/clones/acme_other" add -A
git -C "$WORK_ROOT/clones/acme_other" commit -q -m "Add grain-ci schedule in acme/other"
git -C "$WORK_ROOT/clones/acme_other" push -q origin main
api POST /api/repos/acme/other/lifecycle/refresh '{}' >/dev/null || true
sqlite3 "$DB" "DELETE FROM repo_declaration_sync;"
for _ in $(seq 30); do
  if [ "$(sqlite3 "$DB" "SELECT grain_ci FROM schedule WHERE id='repo:schedule:acme:other:check';")" = "1" ]; then break; fi
  sleep 1
done
test_sweep_requester "2c_cross_repo" "workflow-schedule:repo:schedule:acme:other:check"

echo "--- 2d: schedule with grain-ci: true (and false) without workflow refused / cancelled ---"
mkdir -p "$WORK_ROOT/bad-decl/.grain/templates" "$WORK_ROOT/bad-decl/.grain/schedules"
cp "$WORK_ROOT/clones/acme_widgets/.grain/templates/fix-up.yml" "$WORK_ROOT/bad-decl/.grain/templates/fix-up.yml"
cat >"$WORK_ROOT/bad-decl/.grain/schedules/bad-true.yml" <<'EOF'
template: fix-up
grain-ci: true
every:
  hours: 1
EOF
set +e
"$GRAIN_BIN" declarations check -repo acme/widgets "$WORK_ROOT/bad-decl" >"$WORK_ROOT/out/item2d_decl_true.txt" 2>&1
echo "decl check template + grain-ci: true -> exit=$?: $(cat "$WORK_ROOT/out/item2d_decl_true.txt")"
rm -f "$WORK_ROOT/bad-decl/.grain/schedules/bad-true.yml"
cat >"$WORK_ROOT/bad-decl/.grain/schedules/bad-false.yml" <<'EOF'
template: fix-up
grain-ci: false
every:
  hours: 1
EOF
"$GRAIN_BIN" declarations check -repo acme/widgets "$WORK_ROOT/bad-decl" >"$WORK_ROOT/out/item2d_decl_false.txt" 2>&1
echo "decl check template + grain-ci: false -> exit=$?: $(cat "$WORK_ROOT/out/item2d_decl_false.txt")"
set -e

# Also create a settings schedule row with grain_ci=1 and no workflow, and verify sweep cancels any run claiming it!
SETTINGS_SCHED_JSON=$(api POST /api/schedules '{"title":"Settings task schedule","description":"no workflow","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":6}}')
SETTINGS_SCHED_ID=$(echo "$SETTINGS_SCHED_JSON" | jq -r '.id')
echo "Created settings schedule row: $SETTINGS_SCHED_ID"
sqlite3 "$DB" "UPDATE schedule SET grain_ci = 1 WHERE id = '$SETTINGS_SCHED_ID';"
test_sweep_requester "2d_settings_no_workflow" "workflow-schedule:${SETTINGS_SCHED_ID}"

echo "--- 2e: scheduled release-train workflow finishing green does NOT start promote.yml ---"
rm -f "$WORK_ROOT/clones/acme_widgets/slow.txt"
cat >"$WORK_ROOT/clones/acme_widgets/.grain/workflows/release-train.yml" <<'EOF'
name: release-train
on:
  schedule:
    - cron: "11 2 * * *"
jobs:
  cut:
    steps:
      - name: cut release candidate
        run: echo "release-train ran green"
EOF
cat >"$WORK_ROOT/clones/acme_widgets/.grain/workflows/promote.yml" <<'EOF'
name: promote
on:
  release:
jobs:
  promote:
    steps:
      - name: publish latest
        run: echo "PROMOTE SHOULD NEVER RUN ON CI_MODE GITHUB"
EOF
cat >"$WORK_ROOT/clones/acme_widgets/.grain/schedules/release-train.yml" <<'EOF'
workflow: release-train
grain-ci: true
every:
  hours: 6
EOF
git -C "$WORK_ROOT/clones/acme_widgets" add -A
git -C "$WORK_ROOT/clones/acme_widgets" commit -q -m "Add release-train and promote workflows"
git -C "$WORK_ROOT/clones/acme_widgets" push -q origin main
api POST /api/repos/acme/widgets/lifecycle/refresh '{}' >/dev/null || true
sqlite3 "$DB" "DELETE FROM repo_declaration_sync;"

RT_SCHED="repo:schedule:acme:widgets:release-train"
for _ in $(seq 30); do
  if [ "$(sqlite3 "$DB" "SELECT grain_ci FROM schedule WHERE id='$RT_SCHED';")" = "1" ]; then break; fi
  sleep 1
done
api POST "/api/schedules/$RT_SCHED/run" '{}' >/dev/null
for _ in $(seq 30); do
  SETTLED=$(sqlite3 "$DB" "SELECT review_not_needed FROM scheduled_workflow_run WHERE schedule_id='$RT_SCHED' AND conclusion='success';")
  if [ "$SETTLED" = "1" ]; then break; fi
  sleep 1
done
sleep 3
echo "All ci_run rows on acme/widgets after release-train settled:"
sqlite3 -header -column "$DB" "SELECT id, event, requested_by, conclusion FROM ci_run WHERE repo='acme/widgets' ORDER BY rowid;" | tee "$WORK_ROOT/out/item2e_release_train.txt"
PROMOTE_COUNT=$(sqlite3 "$DB" "SELECT COUNT(*) FROM ci_run WHERE repo='acme/widgets' AND (event='release' OR requested_by LIKE '%promote%');")
echo "promote / release event runs started on acme/widgets: $PROMOTE_COUNT" | tee -a "$WORK_ROOT/out/item2e_release_train.txt"

echo "=== ITEM 3: 'Script on a cadence' shape ==="
# 3a: First try on acme/widgets (ordinary repo) to record what happens when a non-grain repo declares api:tasks:read + api:tasks:from-template!
cat >"$WORK_ROOT/clones/acme_widgets/.grain/workflows/script-cadence.yml" <<'EOF'
name: script-cadence
on:
  schedule:
    - cron: "11 2 * * *"
jobs:
  check-lag:
    capabilities:
      - api:tasks:read
      - api:tasks:from-template
    steps:
      - name: check lag
        run: echo "ran"
EOF
cat >"$WORK_ROOT/clones/acme_widgets/.grain/schedules/script-cadence.yml" <<'EOF'
workflow: script-cadence
grain-ci: true
every:
  hours: 6
EOF
git -C "$WORK_ROOT/clones/acme_widgets" add -A
git -C "$WORK_ROOT/clones/acme_widgets" commit -q -m "Add script-cadence workflow on acme/widgets"
git -C "$WORK_ROOT/clones/acme_widgets" push -q origin main
api POST /api/repos/acme/widgets/lifecycle/refresh '{}' >/dev/null || true
sqlite3 "$DB" "DELETE FROM repo_declaration_sync;"

SC_WIDGETS="repo:schedule:acme:widgets:script-cadence"
for _ in $(seq 30); do
  if [ "$(sqlite3 "$DB" "SELECT grain_ci FROM schedule WHERE id='$SC_WIDGETS';")" = "1" ]; then break; fi
  sleep 1
done
api POST "/api/schedules/$SC_WIDGETS/run" '{}' >/dev/null
for _ in $(seq 20); do
  CONC=$(sqlite3 "$DB" "SELECT conclusion FROM scheduled_workflow_run WHERE schedule_id='$SC_WIDGETS';")
  if [ -n "$CONC" ]; then break; fi
  sleep 1
done
sqlite3 -header -column "$DB" "SELECT schedule_id, run_id, conclusion, detail FROM scheduled_workflow_run WHERE schedule_id='$SC_WIDGETS';" | tee "$WORK_ROOT/out/item3a_widgets_ciapi_refusal.txt"
sqlite3 -header -column "$DB" "SELECT id, conclusion, error FROM ci_run WHERE requested_by='workflow-schedule:$SC_WIDGETS';" | tee -a "$WORK_ROOT/out/item3a_widgets_ciapi_refusal.txt"

# 3b: Now run the full "script on a cadence" shape on ai-on-gke/grain (ci_mode: github, grain-ci: true)!
mkdir -p "$WORK_ROOT/clones/ai-on-gke_grain/.grain/templates" "$WORK_ROOT/clones/ai-on-gke_grain/.grain/workflows" "$WORK_ROOT/clones/ai-on-gke_grain/.grain/schedules"
cat >"$WORK_ROOT/clones/ai-on-gke_grain/.grain/templates/lag-sync.yml" <<'EOF'
title: Sync lagging upstream release
body: |
  Sync the upstream release and open a pull request, or call nothing_needed if up to date.
nothing-needed: close
EOF
cat >"$WORK_ROOT/clones/ai-on-gke_grain/.grain/workflows/release-lag.yml" <<'EOF'
name: release-lag
on:
  schedule:
    - cron: "11 2 * * *"
jobs:
  check-lag:
    capabilities:
      - api:tasks:read
      - api:tasks:from-template
    steps:
      - name: check condition and verify token confinement
        run: |
          set -eu
          if [ -r /home/debian/.grain-api/env ]; then
            echo "sourced /home/debian/.grain-api/env"
            . /home/debian/.grain-api/env
          else
            echo "host-sandbox placement: /home/debian/.grain-api/env absent, sourcing \$HOME/home/debian/.grain-api/env"
            . "$HOME/home/debian/.grain-api/env"
          fi
          # 1. Verify tasks:read works and returns only ai-on-gke/grain tasks
          RAW_TASKS=$(curl -fsS -H "Authorization: Bearer $GRAIN_API_TOKEN" "$GRAIN_API_URL/api/tasks")
          TASKS=$(printf '%s' "$RAW_TASKS" | jq 'if type == "object" and has("items") then .items else . end')
          echo "visible tasks count: $(printf '%s' "$TASKS" | jq 'length')"
          OTHER_REPOS=$(printf '%s' "$TASKS" | jq '[.[] | select(.repo != "ai-on-gke/grain")] | length')
          [ "$OTHER_REPOS" = "0" ] || { echo "LEAK: saw other repo tasks"; exit 1; }

          # 2. Verify free-form POST /api/tasks (outside template) is refused (403)
          DIRECT_CODE=$(curl -sS -o /tmp/direct.out -w '%{http_code}' -X POST \
            -H "Authorization: Bearer $GRAIN_API_TOKEN" -H "Content-Type: application/json" \
            --data '{"title":"freeform","description":"nope","repo":"ai-on-gke/grain"}' \
            "$GRAIN_API_URL/api/tasks")
          echo "POST /api/tasks code=$DIRECT_CODE body=$(cat /tmp/direct.out)"
          [ "$DIRECT_CODE" = "403" ] || { echo "EXPECTED 403 on POST /api/tasks, got $DIRECT_CODE"; exit 1; }

          # 3. Verify ungranted scope GET /api/settings is refused (403)
          SET_CODE=$(curl -sS -o /tmp/set.out -w '%{http_code}' \
            -H "Authorization: Bearer $GRAIN_API_TOKEN" "$GRAIN_API_URL/api/settings")
          echo "GET /api/settings code=$SET_CODE body=$(cat /tmp/set.out)"
          [ "$SET_CODE" = "403" ] || { echo "EXPECTED 403 on GET /api/settings, got $SET_CODE"; exit 1; }

          # 4. Verify cross-repo from-template is refused
          CROSS_CODE=$(curl -sS -o /tmp/cross.out -w '%{http_code}' -X POST \
            -H "Authorization: Bearer $GRAIN_API_TOKEN" -H "Content-Type: application/json" \
            --data '{"repo":"acme/widgets","template":"fix-up"}' \
            "$GRAIN_API_URL/api/tasks/from-template")
          echo "cross-repo from-template code=$CROSS_CODE body=$(cat /tmp/cross.out)"
          [ "$CROSS_CODE" = "403" ] || [ "$CROSS_CODE" = "400" ] || { echo "EXPECTED 403/400 on cross-repo, got $CROSS_CODE"; exit 1; }

          # 5. Check condition: only file lag-sync when lag.txt == 1 and none is already open!
          if [ "$(cat lag.txt)" != "1" ]; then
            echo "lag=0: quiet night, filing nothing"
            exit 0
          fi
          OPEN=$(printf '%s' "$TASKS" | jq '[.[] | select(.title == "Sync lagging upstream release" and .state != "closed")] | length')
          if [ "$OPEN" -gt 0 ]; then
            echo "already open ($OPEN), skipping"
            exit 0
          fi
          FILE_CODE=$(curl -sS -o /tmp/filed.out -w '%{http_code}' -X POST \
            -H "Authorization: Bearer $GRAIN_API_TOKEN" -H "Content-Type: application/json" \
            --data '{"repo":"ai-on-gke/grain","template":"lag-sync"}' \
            "$GRAIN_API_URL/api/tasks/from-template")
          echo "from-template code=$FILE_CODE body=$(cat /tmp/filed.out)"
          [ "$FILE_CODE" = "201" ] || [ "$FILE_CODE" = "200" ]
EOF
cat >"$WORK_ROOT/clones/ai-on-gke_grain/.grain/schedules/release-lag.yml" <<'EOF'
workflow: release-lag
grain-ci: true
every:
  hours: 6
EOF
git -C "$WORK_ROOT/clones/ai-on-gke_grain" add -A
git -C "$WORK_ROOT/clones/ai-on-gke_grain" commit -q -m "Add release-lag script-on-a-cadence schedule"
git -C "$WORK_ROOT/clones/ai-on-gke_grain" push -q origin main
api POST /api/repos/ai-on-gke/grain/lifecycle/refresh '{}' >/dev/null || true
sqlite3 "$DB" "DELETE FROM repo_declaration_sync;"

LAG_SCHED="repo:schedule:ai-on-gke:grain:release-lag"
for _ in $(seq 30); do
  if [ "$(sqlite3 "$DB" "SELECT grain_ci FROM schedule WHERE id='$LAG_SCHED';")" = "1" ]; then break; fi
  sleep 1
done

echo "--- 3b quiet night (lag.txt = 0): runs green, files 0 tasks ---"
api POST "/api/schedules/$LAG_SCHED/run" '{}' >/dev/null
for _ in $(seq 30); do
  SETTLED=$(sqlite3 "$DB" "SELECT review_not_needed FROM scheduled_workflow_run WHERE schedule_id='$LAG_SCHED' AND conclusion='success';")
  if [ "$SETTLED" = "1" ]; then break; fi
  sleep 1
done
QUIET_RUN_ID=$(sqlite3 "$DB" "SELECT run_id FROM scheduled_workflow_run WHERE schedule_id='$LAG_SCHED';")
api GET "/api/ci/runs/$QUIET_RUN_ID" | jq -r '.jobs[0].steps[0].output' >"$WORK_ROOT/out/item3b_quiet_log.txt"
cat "$WORK_ROOT/out/item3b_quiet_log.txt"
GRAIN_TASKS_QUIET=$(api GET /api/tasks | jq '[.[] | select(.repo == "ai-on-gke/grain")] | length')
echo "ai-on-gke/grain tasks after quiet run: $GRAIN_TASKS_QUIET" | tee "$WORK_ROOT/out/item3b_quiet_tasks.txt"

echo "--- 3c work needed (lag.txt = 1): files lag-sync template task ---"
echo "1" >"$WORK_ROOT/clones/ai-on-gke_grain/lag.txt"
git -C "$WORK_ROOT/clones/ai-on-gke_grain" commit -q -am "Simulate upstream lag=1"
git -C "$WORK_ROOT/clones/ai-on-gke_grain" push -q origin main

sqlite3 "$DB" "DELETE FROM scheduled_workflow_run WHERE schedule_id='$LAG_SCHED';"
api POST "/api/schedules/$LAG_SCHED/run" '{}' >/dev/null
for _ in $(seq 30); do
  SETTLED=$(sqlite3 "$DB" "SELECT review_not_needed FROM scheduled_workflow_run WHERE schedule_id='$LAG_SCHED' AND conclusion='success';")
  if [ "$SETTLED" = "1" ]; then break; fi
  sleep 1
done
WORK_RUN_ID=$(sqlite3 "$DB" "SELECT run_id FROM scheduled_workflow_run WHERE schedule_id='$LAG_SCHED';")
api GET "/api/ci/runs/$WORK_RUN_ID" | jq -r '.jobs[0].steps[0].output' >"$WORK_ROOT/out/item3c_work_log.txt"
cat "$WORK_ROOT/out/item3c_work_log.txt"
api GET /api/tasks | jq '[.[] | select(.repo == "ai-on-gke/grain") | {id, title, repo, state, approved, templateId}]' | tee "$WORK_ROOT/out/item3c_filed_tasks.json"

echo "=== ALL ITEMS 1-3 COMPLETED ==="
