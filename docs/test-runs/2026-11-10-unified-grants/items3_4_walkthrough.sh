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

# items3_4_walkthrough.sh: Runs §3 items 3 and 4 against a real `grain daemon
# -host-sandboxes` dispatching `fake-agy.sh` + `hook_3_4.py`:
#   - Item 3: Auto-merge parents holding `api#self-upgrade` (and a second pair
#     holding `api#self-upgrade` + `api#tasks:read`) propose children asking for
#     `self-debug`, `api#self-upgrade`, `api#secrets:write`, and `api#tasks:read`,
#     both live (`hosting: "console"`, `mcpserver -interactive`) and relayed at
#     run end (`hosting: "unattended"`). Verifies person-only children are held
#     without auto-merge, `self-debug` stores its canonical expansion, and live
#     and relayed paths store identical grants, state, and autoMerge.
#   - Item 4: Over a real stdio MCP session (both live and unattended/relayed),
#     calls `propose_task` with `base`, `description`, `fields`, `approve`,
#     `capabilites`, `writes: [{repo, branch}]`, and `dependsOn`; records each
#     refusal, confirms nothing was filed, and captures the relay log output.
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
HEAD_BIN="${HEAD_BIN:-/home/debian/bin/grain}"
OUT_DIR="${OUT_DIR:-$HERE/out}"
WORK="/var/tmp/unified-grants/items3-4"
GHSIM_ADDR="127.0.0.1:4285"
PORT=4286

mkdir -p "$OUT_DIR"
chmod +x "$HERE/fake-agy.sh"

cleanup() {
  for pf in "$WORK/daemon.pid" "$WORK/ghsim.pid"; do
    if [[ -f "$pf" ]]; then
      pid=$(cat "$pf")
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 30); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
    fi
  done
}
trap cleanup EXIT

rm -rf "$WORK"
mkdir -p "$WORK/bin" "$WORK/repos" "$WORK/data" "$WORK/scratch" "$WORK/home" "$WORK/walk/runs"
echo "$WORK/walk" >/var/tmp/unified-grants/current-walk-dir
cp "$HERE/hook_3_4.py" "$WORK/walk/hook.py"

(cd "$ROOT" && go build -o "$WORK/bin/ghsim" "$HERE/ghsim.go")

init_bare() {
  local owner=$1 name=$2 def=$3
  local bare="$WORK/repos/$owner/$name.git"
  mkdir -p "$(dirname "$bare")"
  git init -q --bare -b "$def" "$bare"
  local tmp
  tmp=$(mktemp -d)
  git -C "$tmp" init -q -b "$def"
  echo "# $owner/$name" >"$tmp/README.md"
  git -C "$tmp" add README.md
  git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed"
  git -C "$tmp" push -q "$bare" "$def"
  rm -rf "$tmp"
}
init_bare acme widgets main
init_bare acme lib main

"$WORK/bin/ghsim" -addr "$GHSIM_ADDR" -root "$WORK/repos" \
  -repo acme/widgets=main -repo acme/lib=main \
  >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"

export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0 WALK_DIR="$WORK/walk"
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN

"$HEAD_BIN" daemon -data-dir "$WORK/data" -ui-addr "127.0.0.1:$PORT" -sandbox-port-base=0 \
  -host-sandboxes -sandbox-dir "$WORK/scratch" -sandbox-disk-floor-share 0 -sandbox-disk-floor-min-mb 0 \
  -ci=false -poll-interval 1s -as owner@example.com \
  -default-target-repo acme/widgets \
  -github-host "$GHSIM_ADDR" -github-insecure-http \
  >"$WORK/daemon.log" 2>&1 &
echo $! >"$WORK/daemon.pid"

for _ in $(seq 200); do
  if [[ -s "$WORK/data/local-api-token" ]] && curl -fs -o /dev/null -H "Authorization: Bearer $(tr -d '[:space:]' <"$WORK/data/local-api-token")" "http://127.0.0.1:$PORT/api/config"; then
    break
  fi
  sleep 0.1
done

TOK=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
API="http://127.0.0.1:$PORT"
call() {
  local method=$1 path=$2 body=${3:-}
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOK")
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" --data "$body")
  curl "${args[@]}" "$API$path"
}

call PUT /api/github-tokens/default '{"value":"ghp_walkthrough_stub"}' >/dev/null
for r in acme/widgets acme/lib; do
  call POST /api/repos "{\"repo\":\"$r\"}" >/dev/null
done
call PUT /api/config "{\"agentBackends\":[{\"id\":\"ag-walk\",\"name\":\"Walk Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$HERE/fake-agy.sh\",\"models\":[\"fake-model\"]}]}" >/dev/null
call PUT /api/agent-backends/ag-walk/credential '{"value":"walk-fake-gemini-key"}' >/dev/null
# Set maxWorkers=1 so parents dispatch deterministically one at a time, and pause
# dispatch briefly while filing all 6 parent tasks + writing plan.json!
call PUT /api/settings '{"maxWorkers":1}' >/dev/null

# File unapproved first so we can write plan.json with their exact IDs before approving!
# Note: interactive:true runs `grain mcpserver -interactive` (LIVE propose_task via ui.Client.CreateTask),
# whereas interactive:false runs `grain mcpserver` with MockSink and relays at run end (`relayProposedTasks`)!
P_LIVE_1=$(call POST /api/tasks '{"title":"Item3 Live Parent (self-upgrade only)","description":"§3 item 3","repo":"acme/widgets","approved":false,"autoMerge":true,"interactive":true,"capabilities":["https://grain.dev/capabilities/api#self-upgrade"]}' | jq -r .id)
P_RELAY_1=$(call POST /api/tasks '{"title":"Item3 Relay Parent (self-upgrade only)","description":"§3 item 3","repo":"acme/widgets","approved":false,"autoMerge":true,"interactive":false,"capabilities":["https://grain.dev/capabilities/api#self-upgrade"]}' | jq -r .id)

P_LIVE_2=$(call POST /api/tasks '{"title":"Item3 Live Parent (self-upgrade + tasks:read)","description":"§3 item 3","repo":"acme/widgets","approved":false,"autoMerge":true,"interactive":true,"capabilities":["https://grain.dev/capabilities/api#self-upgrade","https://grain.dev/capabilities/api#tasks:read"]}' | jq -r .id)
P_RELAY_2=$(call POST /api/tasks '{"title":"Item3 Relay Parent (self-upgrade + tasks:read)","description":"§3 item 3","repo":"acme/widgets","approved":false,"autoMerge":true,"interactive":false,"capabilities":["https://grain.dev/capabilities/api#self-upgrade","https://grain.dev/capabilities/api#tasks:read"]}' | jq -r .id)

I4_LIVE=$(call POST /api/tasks '{"title":"Item4 Live Parent","description":"§3 item 4","repo":"acme/widgets","approved":false,"interactive":true,"capabilities":[]}' | jq -r .id)
I4_RELAY=$(call POST /api/tasks '{"title":"Item4 Relay Parent","description":"§3 item 4","repo":"acme/widgets","approved":false,"interactive":false,"capabilities":[]}' | jq -r .id)

jq -n \
  --arg pl1 "$P_LIVE_1" --arg pr1 "$P_RELAY_1" \
  --arg pl2 "$P_LIVE_2" --arg pr2 "$P_RELAY_2" \
  --arg i4l "$I4_LIVE" --arg i4r "$I4_RELAY" \
  '{($pl1):"item3", ($pr1):"item3", ($pl2):"item3", ($pr2):"item3", ($i4l):"item4", ($i4r):"item4"}' \
  >"$WORK/walk/plan.json"

wait_task_done() {
  local tid=$1
  for _ in $(seq 120); do
    local st
    st=$(call GET "/api/tasks/$tid" | jq -r .state)
    if [[ "$st" != "queued" && "$st" != "running" ]]; then
      return 0
    fi
    sleep 0.5
  done
  echo "task $tid timed out waiting to finish; log:" >&2
  tail -n 40 "$WORK/daemon.log" >&2
  exit 1
}

# Approve and run Item 3 parents first (pause auto-submitted children by keeping maxWorkers=1 or approving one parent at a time!).
for tid in "$P_LIVE_1" "$P_RELAY_1" "$P_LIVE_2" "$P_RELAY_2"; do
  echo "Dispatching Item 3 parent task $tid..."
  call POST "/api/tasks/$tid/approve" '{}' >/dev/null
  wait_task_done "$tid"
done

# Snapshot all tasks after Item 3 before running Item 4!
ALL_AFTER_ITEM3=$(call GET "/api/tasks?fields=full")

# Count tasks before Item 4, then dispatch both Item 4 parents!
COUNT_BEFORE_ITEM4=$(jq 'length' <<<"$ALL_AFTER_ITEM3")
for tid in "$I4_LIVE" "$I4_RELAY"; do
  echo "Dispatching Item 4 parent task $tid..."
  call POST "/api/tasks/$tid/approve" '{}' >/dev/null
  wait_task_done "$tid"
done
sleep 1
ALL_AFTER_ITEM4=$(call GET "/api/tasks?fields=full")
COUNT_AFTER_ITEM4=$(jq 'length' <<<"$ALL_AFTER_ITEM4")

RELAY_LOGS=$(grep -E "skipping proposed task" "$WORK/daemon.log" || true)

python3 - "$ALL_AFTER_ITEM3" "$P_LIVE_1" "$P_RELAY_1" "$P_LIVE_2" "$P_RELAY_2" "$OUT_DIR/item3-results.json" <<'PY'
import json, sys

tasks_raw, pl1, pr1, pl2, pr2, out_path = sys.argv[1:]
tasks = json.loads(tasks_raw)
if isinstance(tasks, dict):
    tasks = tasks.get("items") or tasks.get("tasks") or []

def children_of(pid):
    out = {}
    marker = f"task-{pid}: "
    for t in tasks:
        if marker in (t.get("title") or ""):
            label = t["title"].split(marker, 1)[-1]
            # A proposal held for human approval has state == "proposed"; an auto-submitted
            # child has state != "proposed" (queued, running, awaiting_ack, or closed).
            held_for_person = t["state"] == "proposed"
            out[label] = {
                "id": t["id"],
                "title": t["title"],
                "state": t["state"],
                "heldForPerson": held_for_person,
                "autoMerge": bool(t.get("autoMerge")),
                "capabilities": sorted(t.get("capabilities") or []),
            }
    return out

c_live_1 = children_of(pl1)
c_relay_1 = children_of(pr1)
c_live_2 = children_of(pl2)
c_relay_2 = children_of(pr2)

def compare_pair(live_map, relay_map):
    diffs = []
    for k in sorted(set(live_map) | set(relay_map)):
        l, r = live_map.get(k), relay_map.get(k)
        if not l or not r:
            diffs.append(f"missing {k}: live={bool(l)} relay={bool(r)}")
            continue
        for field in ("heldForPerson", "autoMerge", "capabilities"):
            if l[field] != r[field]:
                diffs.append(f"{k}.{field}: live={l[field]} != relay={r[field]}")
    return diffs

res = {
    "pair1_parent_self_upgrade_only": {
        "liveParentId": pl1,
        "relayParentId": pr1,
        "liveChildren": c_live_1,
        "relayChildren": c_relay_1,
        "parityDiffs": compare_pair(c_live_1, c_relay_1),
    },
    "pair2_parent_self_upgrade_and_tasks_read": {
        "liveParentId": pl2,
        "relayParentId": pr2,
        "liveChildren": c_live_2,
        "relayChildren": c_relay_2,
        "parityDiffs": compare_pair(c_live_2, c_relay_2),
    },
}
open(out_path, "w").write(json.dumps(res, indent=2) + "\n")
print("Item 3 parityDiffs pair1:", res["pair1_parent_self_upgrade_only"]["parityDiffs"])
print("Item 3 parityDiffs pair2:", res["pair2_parent_self_upgrade_and_tasks_read"]["parityDiffs"])
PY

jq -n \
  --argjson countBefore "$COUNT_BEFORE_ITEM4" \
  --argjson countAfter "$COUNT_AFTER_ITEM4" \
  --slurpfile liveMcp "$WORK/walk/runs/mcp-results-task-${I4_LIVE}.json" \
  --slurpfile relayMcp "$WORK/walk/runs/mcp-results-task-${I4_RELAY}.json" \
  --arg relayDaemonLog "$RELAY_LOGS" \
  --argjson relayParentComments "$(call GET "/api/tasks/$I4_RELAY" | jq '[.comments[]? | {author, body}]')" \
  '{
    tasksBeforeItem4: $countBefore,
    tasksAfterItem4: $countAfter,
    newlyFiledCount: ($countAfter - $countBefore),
    liveMcpSession: $liveMcp[0],
    relayMcpSession: $relayMcp[0],
    relayDaemonLog: ($relayDaemonLog | split("\n") | map(select(length > 0))),
    relayParentComments: $relayParentComments
  }' >"$OUT_DIR/item4-results.json"

echo "Items 3 & 4 DONE."
