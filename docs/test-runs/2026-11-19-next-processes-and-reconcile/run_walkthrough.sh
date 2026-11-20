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

# Runs the complete 2026-11-19 §4 walkthrough (UI parity + scoped RBAC +
# daemon woken-cycle scheduling) against a real grain daemon on STORE=sqlite
# or STORE=postgres.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

STORE=${STORE:-sqlite}
WORK_DIR=${WORK_DIR:-/tmp/walk-proc-${STORE}}
export STORE WORK_DIR

trap 'stop_env "$WORK_DIR"' EXIT

echo "=== [${STORE}] Phase 1: Init env, sim, real daemon (POLL_INTERVAL=1s) ==="
init_env "$WORK_DIR"
start_sim "$WORK_DIR"
POLL_INTERVAL=1s MAX_WORKERS=1 start_daemon "$WORK_DIR"
seed_rbac_and_repos

BOB_JWT=$(mint_iap "bob@example.com")
CAROL_JWT=$(mint_iap "carol@example.com")

echo "=== [${STORE}] Phase 2: Browser walkthrough (/next/processes, RBAC, /next/agents, sweeps) ==="
(
	cd "$ROOT"
	DAEMON_URL="$DAEMON" \
	ACCESS_CODE="$CODE" \
	BOB_JWT="$BOB_JWT" \
	CAROL_JWT="$CAROL_JWT" \
	STORE="$STORE" \
	OUT_DIR="$WORK_DIR/ui" \
	PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
	node "$HERE/walk_ui.mjs"
)

echo "=== [${STORE}] Phase 3: Daemon woken-cycle scheduling (POLL_INTERVAL=30s, MAX_WORKERS=1) ==="
# Update stored pollInterval to 30s (grain_config overrides -poll-interval on restart!)
# and close any leftover Phase 2 tasks so Phase 3 starts completely quiet.
api PUT /api/settings '{"pollInterval":"30s"}' >/dev/null
python3 - "$DAEMON" "$CODE" <<'PY'
import json, sys, urllib.request
daemon, code = sys.argv[1:3]
def req(m, p, b=None):
    d = json.dumps(b).encode() if b is not None else None
    r = urllib.request.Request(daemon + p, data=d, method=m, headers={"Authorization": f"Bearer {code}", **({"Content-Type": "application/json"} if d else {})})
    with urllib.request.urlopen(r, timeout=15) as resp:
        return json.loads(resp.read().decode())
tasks = req("GET", "/api/tasks")
if isinstance(tasks, dict):
    tasks = tasks.get("tasks", [])
for t in tasks:
    if t.get("state") not in ("closed", "merged"):
        try:
            req("POST", f"/api/tasks/{t['id']}/close", {"reason": "clean up before Phase 3"})
        except Exception:
            pass
PY
rm -f "$WORK_DIR/runs/turns.jsonl" "$WORK_DIR/runs/"*.marker "$WORK_DIR/runs/"*.release "$WORK_DIR/action-"*.json
POLL_INTERVAL=30s MAX_WORKERS=1 start_daemon "$WORK_DIR"

# Wait for initial full RunCycle (cycles.observed >= 1)
python3 - "$DAEMON" "$CODE" "$WORK_DIR" "$STORE" <<'PY'
import json
import os
import sys
import time
import urllib.request

daemon, code, work_dir, store = sys.argv[1:5]
runs_dir = os.path.join(work_dir, "runs")
os.makedirs(runs_dir, exist_ok=True)

def req(method, path, body=None):
    data = json.dumps(body).encode("utf-8") if body is not None else None
    r = urllib.request.Request(
        daemon + path,
        data=data,
        method=method,
        headers={
            "Authorization": f"Bearer {code}",
            **({"Content-Type": "application/json"} if data is not None else {}),
        },
    )
    with urllib.request.urlopen(r, timeout=15) as resp:
        return json.loads(resp.read().decode("utf-8"))

def get_cycles():
    m = req("GET", "/api/metrics")
    return m.get("cycles", {})

def read_turns():
    p = os.path.join(runs_dir, "turns.jsonl")
    if not os.path.exists(p):
        return []
    with open(p, "r", encoding="utf-8") as f:
        return [json.loads(line) for line in f if line.strip()]

def wait_for(desc, pred, timeout=25.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        val = pred()
        if val:
            return val
        time.sleep(0.05)
    raise RuntimeError(f"timed out waiting for: {desc}")

# 1. Wait for initial RunCycle
def poll_initial():
    c = get_cycles()
    return c if c.get("observed", 0) >= 1 else None

c0 = wait_for("initial RunCycle", poll_initial, timeout=20.0)
initial_observed = c0["observed"]
print(f"[{store}] Initial cycles observed={initial_observed}")

# Set default fast action for tasks without explicit action files
with open(os.path.join(work_dir, "action-default.json"), "w", encoding="utf-8") as f:
    json.dump({"kind": "plain_reply", "response": "fast default"}, f)

# ---------------------------------------------------------------------------
# Test 3A: Slow-unwinding interactive run + human reply during unwind
# ---------------------------------------------------------------------------
# Create a standalone interactive task (noRepo=True avoids branchExistsSettled's
# 8s GitHub 404 retry so our controlled wait_for_file unwind measures the exact
# InFlight.SubscribeDone wake latency cleanly against PollInterval=30s).
t1 = req("POST", "/api/tasks", {
    "title": "Woken interactive slow-unwind task",
    "description": "Test InFlight.SubscribeDone wake on human reply during unwind",
    "noRepo": True,
    "interactive": True,
    "holdForApproval": True,
})
t1_id = str(t1["id"])
with open(os.path.join(work_dir, f"action-{t1_id}.json"), "w", encoding="utf-8") as f:
    json.dump([
        {
            "kind": "plain_reply",
            "response": "turn 1 response before slow unwind",
            "start_marker": "t1-turn1.marker",
            "wait_for_file": "t1-turn1.release",
            "wait_timeout_sec": 15.0,
        },
        {
            "kind": "plain_reply",
            "response": "turn 2 immediate after unwind",
            "start_marker": "t1-turn2.marker",
        },
    ], f)

t1_approve_at = time.time()
req("POST", f"/api/tasks/{t1_id}/approve", {})

# Wait for turn 1 to enter fake-agy
wait_for("t1 turn 1 start marker", lambda: os.path.exists(os.path.join(runs_dir, "t1-turn1.marker")), timeout=10.0)
t1_turn1_started = json.loads(open(os.path.join(runs_dir, "t1-turn1.marker")).read())["started_ts"]
print(f"[{store}] t1 ({t1_id}) turn 1 started {t1_turn1_started - t1_approve_at:.3f}s after approve")

# Sleep 0.3s (> minWakeInterval=50ms) so fast-wake timer would have expired without SubscribeDone,
# then post a human reply WHILE turn 1 is still unwinding!
time.sleep(0.3)
reply_posted_at = time.time()
req("POST", f"/api/tasks/{t1_id}/comments", {"body": "follow-up during unwind"})

# Keep turn 1 unwinding for another 0.4s after the reply arrived so the wake definitely saw hadInFlight=True
time.sleep(0.4)
released_at = time.time()
with open(os.path.join(work_dir, "runs", "t1-turn1.release"), "w", encoding="utf-8") as f:
    f.write("go\n")

# Wait for turn 2 to start promptly via InFlight.SubscribeDone!
wait_for("t1 turn 2 start marker", lambda: os.path.exists(os.path.join(runs_dir, "t1-turn2.marker")), timeout=10.0)
t1_turn2_started = json.loads(open(os.path.join(runs_dir, "t1-turn2.marker")).read())["started_ts"]
wait_for("t1 turn 2 finished in turns.jsonl", lambda: len([x for x in read_turns() if x["task_id"] == t1_id]) >= 2, timeout=10.0)
t1_turns = [x for x in read_turns() if x["task_id"] == t1_id]
turn2_gap_from_release = t1_turn2_started - released_at
turn2_gap_from_turn1_end = t1_turn2_started - t1_turns[0]["finished_ts"]
turn2_saw_followup = t1_turns[1]["has_followup_text"]
print(
    f"[{store}] [3A] t1 turn2 started {turn2_gap_from_release:.3f}s after release "
    f"({turn2_gap_from_turn1_end:.3f}s after turn1 exit), saw_followup={turn2_saw_followup}"
)

# ---------------------------------------------------------------------------
# Test 3B: Second task queued behind slow worker + burst of fast dispatches
#          does NOT starve a pending woken full RunCycle!
# ---------------------------------------------------------------------------
# Wait for t1's woken full cycle to complete first (headStart = 5s)
wait_for(
    "woken full cycle after t1",
    lambda: get_cycles().get("observed", 0) > initial_observed,
    timeout=12.0,
)
base_cycles = get_cycles().get("observed", 0)
print(f"[{store}] Baseline cycles before burst test: observed={base_cycles}")

# Fire a burst of fast interactive tasks every 1.1s for ~6.6s (while PollInterval=30s)
burst_start = time.time()
cycles_at_1_5s = None
burst_tasks = []
for i in range(6):
    bt = req("POST", "/api/tasks", {
        "title": f"Burst interactive task {i+1}",
        "description": "Fast interactive dispatch during pending woken cycle",
        "noRepo": True,
        "interactive": True,
        "approved": True,
    })
    burst_tasks.append(str(bt["id"]))
    time.sleep(1.1)
    if i == 0:
        cycles_at_1_5s = get_cycles().get("observed", 0)

# By now ~6.6s have elapsed since burst_start (headStart=5s, PollInterval=30s).
# If later dispatches had re-armed the woken full cycle, it wouldn't run until >= 11.6s!
def poll_after_burst():
    c = get_cycles()
    return c if c.get("observed", 0) > base_cycles else None

c_after_burst = wait_for(
    "pending woken full RunCycle not starved by dispatch burst",
    poll_after_burst,
    timeout=10.0,
)
woken_cycle_elapsed = time.time() - burst_start
print(
    f"[{store}] [3B] cycles_at_1_5s={cycles_at_1_5s} (base={base_cycles}), "
    f"woken full cycle observed={c_after_burst['observed']} at +{woken_cycle_elapsed:.2f}s "
    f"(PollInterval=30s)"
)

out = {
    "store": store,
    "poll_interval_sec": 30,
    "initial_observed": initial_observed,
    "test_3a_slow_unwind_reply": {
        "pass": (turn2_gap_from_release < 3.0) and bool(turn2_saw_followup),
        "task_id": t1_id,
        "turn2_gap_from_release_sec": round(turn2_gap_from_release, 3),
        "turn2_gap_from_turn1_end_sec": round(turn2_gap_from_turn1_end, 3),
        "turn2_saw_followup": turn2_saw_followup,
    },
    "test_3b_burst_no_starvation": {
        "pass": (cycles_at_1_5s == base_cycles) and (woken_cycle_elapsed < 10.5),
        "base_cycles": base_cycles,
        "cycles_at_1_5s": cycles_at_1_5s,
        "after_burst_observed": c_after_burst["observed"],
        "woken_cycle_elapsed_sec": round(woken_cycle_elapsed, 3),
        "burst_tasks": burst_tasks,
    },
}
with open(os.path.join(work_dir, "reconcile-report.json"), "w", encoding="utf-8") as f:
    json.dump(out, f, indent=2)
print(json.dumps(out, indent=2))
PY
