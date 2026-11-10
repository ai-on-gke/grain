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

# walk_item_6_nothing_needed.sh exercises §4 item 6:
#   - syncs the real `.grain/schedules/` and `.grain/templates/` declarations
#     from the repo into a simulated `ai-on-gke/grain` repository on a real
#     `grain daemon -sandbox-port-base=0 -host-sandboxes -reconcile`
#   - fires each `overlap: skip` template schedule with a fake agent calling
#     `nothing_needed` over the run's real MCP stdio server, confirming that
#     every task transitions straight to `closed` (never `awaiting_reply`)
#     and that an immediate second `overlap: skip` firing is NOT held
#   - runs a pre-528336c1 control schedule whose agent calls `comment_on_issue`
#     with no pushed branch, confirming that it parks in `awaiting_reply` and
#     holds the next `overlap: skip` firing (HTTP 409).
set -euo pipefail

GRAIN_BIN=${GRAIN_BIN:-/home/debian/bin/grain}
SIM_BIN=${SIM_BIN:-/home/debian/bin/githubsim_server}
REPO_SRC=${REPO_SRC:-/home/debian/work}
WORK_ROOT=${WORK_ROOT:-/tmp/walk-item6}
SIM_PORT=${SIM_PORT:-4290}
UI_PORT=${UI_PORT:-4291}
SIM_URL="http://127.0.0.1:${SIM_PORT}"
API_URL="http://127.0.0.1:${UI_PORT}"

cleanup() {
  set +e
  [ -f "$WORK_ROOT/daemon.pid" ] && kill "$(cat "$WORK_ROOT/daemon.pid")" 2>/dev/null || true
  [ -f "$WORK_ROOT/sim.pid" ] && kill "$(cat "$WORK_ROOT/sim.pid")" 2>/dev/null || true
}
trap cleanup EXIT

rm -rf "$WORK_ROOT"
mkdir -p "$WORK_ROOT/repos" "$WORK_ROOT/data" "$WORK_ROOT/sb" "$WORK_ROOT/clone" "$WORK_ROOT/out"

setsid "$SIM_BIN" \
  -addr "127.0.0.1:${SIM_PORT}" \
  -repos-dir "$WORK_ROOT/repos" \
  -repos "ai-on-gke/grain" \
  -token "sim-token" \
  >"$WORK_ROOT/sim.log" 2>&1 </dev/null &
echo $! >"$WORK_ROOT/sim.pid"

for _ in $(seq 30); do
  curl -fsS "$SIM_URL/_sim/state" >/dev/null 2>&1 && break
  sleep 0.2
done

# Seed ai-on-gke/grain with the real .grain/ directory from HEAD.
git -C "$WORK_ROOT/clone" init -q -b main
git -C "$WORK_ROOT/clone" config user.name "Alice Example"
git -C "$WORK_ROOT/clone" config user.email "alice@example.com"
git -C "$WORK_ROOT/clone" remote add origin "http://sim-token:@127.0.0.1:${SIM_PORT}/ai-on-gke/grain.git"
cp -a "$REPO_SRC/.grain" "$WORK_ROOT/clone/.grain"
echo "seed" >"$WORK_ROOT/clone/README.md"
git -C "$WORK_ROOT/clone" add -A
git -C "$WORK_ROOT/clone" commit -q -m "Seed real .grain declarations"
git -C "$WORK_ROOT/clone" push -q -u origin main

# Write MCP caller + fake-agy.
echo "nothing_needed" >"$WORK_ROOT/finish_mode"
cat >"$WORK_ROOT/mcp_finish.py" <<'PYEOF'
import json, os, subprocess, sys

mode_file = sys.argv[1]
with open(mode_file) as f:
    mode = f.read().strip()

mcp_path = os.path.expanduser("~/.gemini/config/mcp_config.json")
cfg = json.load(open(mcp_path))["mcpServers"]["grain-sandbox"]
p = subprocess.Popen([cfg["command"]] + cfg["args"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)

def send(msg):
    p.stdin.write(json.dumps(msg) + "\n")
    p.stdin.flush()

def recv(want_id):
    while True:
        line = p.stdout.readline()
        if not line:
            return {}
        m = json.loads(line)
        if m.get("id") == want_id:
            return m

send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
    "protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "fake-agy", "version": "0"}}})
recv(1)
send({"jsonrpc": "2.0", "method": "notifications/initialized"})

if mode == "comment_on_issue":
    tool = "comment_on_issue"
    args = {"comment": "Control run finished with bare comment_on_issue"}
else:
    tool = "nothing_needed"
    args = {"reason": "Walkthrough verified branchless nothing_needed close"}

send({"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": tool, "arguments": args}})
r = recv(2)
text = "\n".join(c.get("text", "") for c in r.get("result", {}).get("content", [])) or json.dumps(r.get("error"))
name = "mcp_grain-sandbox_" + tool
base = {"conversation_id": "fake-agy-item6", "step_index": 1, "step_type": "tool", "tool_name": name}
for state, extra in [("ACTIVE", {}), ("DONE", {"output": text})]:
    print(json.dumps({"event": "step_update", "step_update": {**base, "state": state, "tool_info": {"name": name, "parameters": args, **extra}}}))

p.stdin.close()
p.terminate()
PYEOF

cat >"$WORK_ROOT/fake-agy.sh" <<EOF
#!/usr/bin/env bash
for arg in "\$@"; do
  case "\$arg" in
    --help) exit 0;;
    --version) echo 'fake agy (item6)'; exit 0;;
    models)
      printf 'gemini-3.1-pro\nfable-5.1-high\n'
      exit 0;;
  esac
done
IFS= read -r _ || true
printf '%s\n' '{"event":"init","conversation_id":"fake-agy-item6","init":{"tools":[],"permission_mode":"always-proceed"}}'
python3 "$WORK_ROOT/mcp_finish.py" "$WORK_ROOT/finish_mode"
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-item6","step_index":2,"step_type":"agent_response","state":"ACTIVE","text_delta":"done"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-item6","step_index":2,"step_type":"agent_response","state":"DONE"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"fake-agy-item6","status":"SUCCESS","response":"done","num_turns":1}}'
EOF
chmod +x "$WORK_ROOT/fake-agy.sh"
echo "dummy-gemini-key" >"$WORK_ROOT/gem.key"

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
  -gemini-api-key-file "$WORK_ROOT/gem.key" \
  >"$WORK_ROOT/daemon.log" 2>&1 </dev/null &
echo $! >"$WORK_ROOT/daemon.pid"

for _ in $(seq 45); do
  kill -0 "$(cat "$WORK_ROOT/daemon.pid")" 2>/dev/null || { echo "daemon exited:"; tail -n 40 "$WORK_ROOT/daemon.log"; exit 1; }
  if [ -s "$WORK_ROOT/data/local-api-token" ]; then
    TOK=$(cat "$WORK_ROOT/data/local-api-token")
    if curl -fs -o /dev/null -H "Authorization: Bearer $TOK" "$API_URL/api/config"; then break; fi
  fi
  sleep 0.4
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
    curl -sS -o "$WORK_ROOT/last.json" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" --data "$body" "$API_URL$path"
  else
    curl -sS -o "$WORK_ROOT/last.json" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $TOK" "$API_URL$path"
  fi
}

# Pause system schedules and close the seeded director task so only the schedules we fire run on the worker!
for sys_id in $(api GET /api/schedules | jq -r '.[] | select(.id | startswith("system:")) | .id'); do
  api PATCH "/api/schedules/$sys_id" '{"enabled":false}' >/dev/null || true
done
api POST /api/tasks/1/close '{"reason":"unused in walkthrough"}' >/dev/null || true

api PUT /api/settings '{"agentFramework":"antigravity","agentBackends":[{"id":"agy","framework":"antigravity"}]}' >/dev/null
api PUT /api/agent-backends/agy/credential '{"value":"dummy-gemini-key"}' >/dev/null || true
api PUT /api/agent-keys/antigravity '{"value":"dummy-gemini-key"}' >/dev/null || true
api PUT /api/github-tokens/default '{"value":"sim-token"}' >/dev/null
api POST /api/repos '{"repo":"ai-on-gke/grain"}' >/dev/null
api PUT /api/repos/ai-on-gke/grain/ci-mode '{"ciMode":"github"}' >/dev/null
api PUT /api/repos/ai-on-gke/grain/declarations '{"declarations":true}' >/dev/null

echo "=== Waiting for ai-on-gke/grain declarations sync ==="
for _ in $(seq 30); do
  if api GET /api/schedules | jq -e '.[] | select(.id == "repo:schedule:ai-on-gke:grain:weekly-dead-code")' >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

AUDITED_STEMS=(
  release-train
  release-watchdog
  release-process-review
  prune-packages
  nightly-security-review
  nightly-features-test-plan
  weekly-dead-code
  ui-walkthrough-review
  update-agent-clients
)

printf "%-28s | %-6s | %-10s | %-12s | %-10s\n" "SCHEDULE" "RUN1" "STATE1" "RUN2_HTTP" "STATE2" | tee "$WORK_ROOT/out/item6_results.txt"
printf "%-28s-+-%-6s-+-%-10s-+-%-12s-+-%-10s\n" "----------------------------" "------" "----------" "------------" "----------" | tee -a "$WORK_ROOT/out/item6_results.txt"

for stem in "${AUDITED_STEMS[@]}"; do
  sid="repo:schedule:ai-on-gke:grain:${stem}"
  # Pause cadence so only our explicit firings run.
  api PATCH "/api/schedules/$sid" '{"enabled":false}' >/dev/null || true

  code1=$(api_code POST "/api/schedules/$sid/run" '{}')
  t1=$(jq -r '.taskId // empty' "$WORK_ROOT/last.json")
  if { [ "$code1" != "200" ] && [ "$code1" != "201" ]; } || [ -z "$t1" ]; then
    echo "FAIL firing 1 of $stem: HTTP $code1 $(cat "$WORK_ROOT/last.json")"
    exit 1
  fi

  state1=""
  for _ in $(seq 45); do
    state1=$(api GET "/api/tasks/$t1" | jq -r '.state')
    if [ "$state1" = "closed" ] || [ "$state1" = "awaiting_reply" ] || [ "$state1" = "failed" ]; then
      break
    fi
    sleep 0.5
  done

  # Immediately fire the overlap: skip schedule a SECOND time!
  code2=$(api_code POST "/api/schedules/$sid/run" '{}')
  t2=$(jq -r '.taskId // empty' "$WORK_ROOT/last.json")
  state2="n/a"
  if [ -n "$t2" ]; then
    for _ in $(seq 45); do
      state2=$(api GET "/api/tasks/$t2" | jq -r '.state')
      if [ "$state2" = "closed" ] || [ "$state2" = "awaiting_reply" ] || [ "$state2" = "failed" ]; then
        break
      fi
      sleep 0.5
    done
  fi

  printf "%-28s | %-6s | %-10s | %-12s | %-10s\n" "$stem" "t#$t1" "$state1" "HTTP $code2" "$state2" | tee -a "$WORK_ROOT/out/item6_results.txt"
  [ "$state1" = "closed" ] || { echo "FAIL: $stem landed in $state1 instead of closed! Log:"; tail -n 40 "$WORK_ROOT/daemon.log"; exit 1; }
  [ "$code2" = "200" ] || [ "$code2" = "201" ] || { echo "FAIL: second overlap:skip firing of $stem returned HTTP $code2"; exit 1; }
  [ "$state2" = "closed" ] || { echo "FAIL: second run of $stem landed in $state2"; exit 1; }
done

echo "=== Contrast control: comment_on_issue on overlap: skip schedule parks in awaiting_reply and holds next firing ==="
echo "comment_on_issue" >"$WORK_ROOT/finish_mode"
CTRL_JSON=$(api POST /api/schedules '{"title":"Control bare comment schedule","description":"test","repo":"ai-on-gke/grain","overlap":"skip","nothingNeededAction":"close","recurrence":{"kind":"everyNHours","everyNHours":6}}')
CTRL_ID=$(echo "$CTRL_JSON" | jq -r '.id')
api PATCH "/api/schedules/$CTRL_ID" '{"enabled":false}' >/dev/null

c1=$(api_code POST "/api/schedules/$CTRL_ID/run" '{}')
ct1=$(jq -r '.taskId' "$WORK_ROOT/last.json")
cstate1=""
for _ in $(seq 45); do
  cstate1=$(api GET "/api/tasks/$ct1" | jq -r '.state')
  if [ "$cstate1" = "awaiting_reply" ] || [ "$cstate1" = "closed" ]; then break; fi
  sleep 0.5
done
c2=$(api_code POST "/api/schedules/$CTRL_ID/run" '{}')
echo "Control (comment_on_issue): task=$ct1 state=$cstate1; second firing -> HTTP $c2 $(cat "$WORK_ROOT/last.json")" | tee "$WORK_ROOT/out/item6_control.txt"
[ "$cstate1" = "awaiting_reply" ] || { echo "Expected awaiting_reply on control, got $cstate1"; exit 1; }
[ "$c2" = "409" ] || { echo "Expected 409 on held overlap:skip control, got $c2"; exit 1; }

echo "=== ITEM 6 COMPLETED SUCCESSFULLY ==="
