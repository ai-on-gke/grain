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

# item4.sh: the items task 5726 left unwalked (task 5896).
#   A. the grain/config export v3.5.0 wrote (seed.sh's data dir, run on in
#      place: WORK as seed.sh had it), with bare names and a
#      settings/capabilities.yml naming grain-ci and self-debug committed
#      by hand, then opened by HEAD (BIN): the in-place upgrade merges the
#      commits the store had not applied and rewrites the dump.
#   B. the catalogue entries at filing and dispatch (MCP tools, grain api
#      routes, grain ls src://, grain ci log), and a run's propose_task
#      naming self-debug and grain-ci (F5) followed to where it is filed.
#   C. a .grain/workflows job declaring a bare name on a ci_mode: grain repo.
#   D. local policy deny: [self-debug] and deny: ["gcp-key*"].
set -euo pipefail
source "$(dirname "$0")/common.sh"
[[ -d "$WORK/data/state-repo" ]] || { echo "run seed.sh first (WORK=$WORK)"; exit 1; }
rm -f "$WORK/walk/runs/"*
(cd "$ROOT" && go build -o "$WORK/ghsim" ./docs/test-runs/2026-11-14-retired-capability-aliases/ghsim.go)
mkdir -p /var/tmp/retired-aliases; echo "$WORK/walk" >/var/tmp/retired-aliases/current-walk-dir
cp "$HERE/hook.py" "$WORK/walk/hook.py"
GCP_GRANT='https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"default","primary":true}'

# A. The v3.5.0 export, with bare names hand-added where an operator
# editing grain/config by hand would put them.
CS="$WORK/data/state-repo"
python3 - "$CS" <<'EOF'
import json, sys, re
d = sys.argv[1]
t = open(d + "/settings/templates.yml").read()
t = t.replace("    grants:\n", "    grants:\n      - self-debug\n      - grain-ci\n      - read-issues\n      - github-credential:bot\n", 1)
open(d + "/settings/templates.yml", "w").write(t)
s = open(d + "/settings/schedules.yml").read()
s = s.replace("    grants:\n", "    grants:\n      - bootstrap-playbooks\n      - grain-log\n", 1)
open(d + "/settings/schedules.yml", "w").write(s)
cfg = json.load(open(d + "/tables/grain_config.json"))
for row in cfg:
    row["default_capabilities"] += ",self-debug,read-issues"
json.dump(cfg, open(d + "/tables/grain_config.json", "w"), indent=2)
rc = json.load(open(d + "/tables/repo_config.json"))
for row in rc:
    if row["name"] == "widgets":
        row["ci_mode"] = "grain"
        row["ci_capabilities"] = "grain-log,self-debug,github-credential:bot"
        row["default_capabilities"] = "github-sandbox"
json.dump(rc, open(d + "/tables/repo_config.json", "w"), indent=2)
open(d + "/settings/capabilities.yml", "w").write("""in_process:
  grain-ci:
    enabled: false
  self-debug:
    restrictions:
      repos: [acme/lib]
""")
EOF
git -C "$CS" -c user.name=w -c user.email=w@example.com commit -qam "hand-add bare names"
git -C "$CS" add -A && git -C "$CS" -c user.name=w -c user.email=w@example.com commit -qm "add settings/capabilities.yml"
trap '[[ -f "$WORK/daemon.pid" ]] && stop; kill "$(cat "$WORK/ghsim.pid")" 2>/dev/null || true' EXIT
start_ghsim; env_world
{
echo "== version"; "$BIN" version 2>&1 | head -1
echo "== A. hand-added bare names in the v3.5.0 export"
echo "schema-version: $(cat "$CS/schema-version")"; git -C "$CS" show --stat --format='%s' HEAD~1 HEAD | grep -v '^$'
grep -rn -E '(^|[ ,"-])(self-debug|grain-ci|read-issues|github-credential:bot|bootstrap-playbooks|grain-log|github-sandbox)([,"]|$)' \
  "$CS/settings" "$CS/tables/grain_config.json" "$CS/tables/repo_config.json" | sed "s|$CS/||" | grep -v k8s_priority_class | cut -c1-200
: >"$WORK/daemon.log"
start; sleep 3
echo "-- daemon log lines about the upgrade and import"; grep -i 'state repo\|config repo\|migrat\|schema\|retired\|capabilities.yml' "$WORK/daemon.log" | cut -c1-500 | head -20
echo "schema-version now: $(cat "$CS/schema-version")"; git -C "$CS" log --format='%h %s' -4
echo "-- rows after HEAD applied it"
echo "template_grant:"; q "SELECT capability FROM template_grant ORDER BY 1"
echo "schedule_grant:"; q "SELECT capability FROM schedule_grant WHERE schedule_id NOT LIKE 'system:%' ORDER BY 1"
echo "grain_config.default_capabilities:"; q "SELECT default_capabilities FROM grain_config"
echo "repo_config:"; q "SELECT owner, name, ci_mode, default_capabilities, ci_capabilities FROM repo_config ORDER BY 2"
echo "== A. the dump HEAD rewrote (schema $(cat "$CS/schema-version")): bare names left (local-scripts:deploy is F3's)"
grep -rn -E '(^|[ ,"-])(self-debug|grain-ci|read-issues|github-credential:bot|bootstrap-playbooks|grain-log|github-sandbox)([,"]|$)' \
  "$CS/settings" "$CS/tables" | sed "s|$CS/||" | grep -v k8s_priority_class | cut -c1-260 || echo "(none)"
echo "-- settings/capabilities.yml as re-exported"; cat "$CS/settings/capabilities.yml" 2>/dev/null || echo "(not written)"
echo "-- template grants as re-exported"; grep -n -A14 '    grants:' "$CS/settings/templates.yml" | head -16
# The seeded deployment defaults hold github#sandbox-repo, which needs a
# GitHub App this walk has none of: clear them so the runs below dispatch.
call PUT /api/config '{"defaultCapabilities":[]}' | jq -c '{error, defaultCapabilities}'

echo "== B. catalogue at filing (in_process.self-debug restricted to acme/lib, in_process.grain-ci disabled)"
SD='["https://grain.dev/capabilities/api#self-debug","https://grain.dev/capabilities/grain#source={}"]'
printf 'widgets + api#self-debug -> '; call POST /api/tasks "$(jq -nc --argjson c "$SD" '{title:"cat widgets self-debug",description:"walk",repo:"acme/widgets",capabilities:$c,approve:true}')" | jq -c '{id,state,error}'
printf 'lib + api#self-debug -> '; LIB=$(call POST /api/tasks "$(jq -nc --argjson c "$SD" '{title:"cat lib self-debug",description:"walk",repo:"acme/lib",capabilities:$c,approve:false}')"); echo "$LIB" | jq -c '{id,state,error}'
printf 'widgets plain (ci_mode grain) -> '; WID=$(call POST /api/tasks '{"title":"cat widgets plain","description":"walk","repo":"acme/widgets","approve":false}'); echo "$WID" | jq -c '{id,state,error}'
LIB_ID=$(echo "$LIB" | jq -r .id); WID_ID=$(echo "$WID" | jq -r .id)
for id in $LIB_ID $WID_ID; do printf '%s: ' "$id"; call GET /api/tasks/$id | jq -c '{capabilities, grants: [.grants[]? | (.capability // .)]}'; done
echo "== B. dispatch"
call PUT /api/config "{\"agentBackends\":[{\"id\":\"ag-walk\",\"name\":\"Walk Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$HERE/fake-agy.sh\",\"models\":[\"fake-model\"]}]}" | jq -c '.error'
call PUT /api/agent-backends/ag-walk/credential '{"value":"walk-fake-gemini-key"}' >/dev/null
for id in $LIB_ID $WID_ID; do call POST /api/tasks/$id/approve '{}' | jq -c '{id,state,error}'; done
for id in $LIB_ID $WID_ID; do
  for _ in $(seq 360); do [[ -f "$WORK/walk/runs/hook-$id.json" ]] && break; sleep 0.5; done
  echo "-- task $id"; jq -c '{tools}' "$WORK/walk/runs/hook-$id.json" 2>/dev/null || { echo "no hook output"; call GET /api/tasks/$id | jq -c '{state, last: ([.comments[]?.body] | last | .[0:500])}'; }
  jq -r 'del(.tools) | to_entries[] | "\(.key): \(.value.text | gsub("\n"; " | ") | .[0:600])"' "$WORK/walk/runs/hook-$id.json" 2>/dev/null || true
done
echo "== B. F5: the run's propose_task(self-debug, grain-ci) (f5.sh walks the build before the fix)"
for _ in $(seq 120); do s=$(call GET /api/tasks/$LIB_ID | jq -r .state); [[ "$s" != running && "$s" != queued ]] && break; sleep 1; done
echo "task $LIB_ID state: $s"
call GET '/api/tasks?limit=200' | jq -c '[.[]? // .tasks[]? | select(.title == "walk: bare names") | {id, state}]'
for id in $(call GET '/api/tasks?limit=200' | jq -r '[.[]? // .tasks[]? | select(.title == "walk: bare names") | .id] | .[]'); do
  call GET /api/tasks/$id | jq -c '{id, grants: [.grants[]? | (.capability // .)], body: .description[0:900]}'
done
for id in $LIB_ID $WID_ID; do printf '%s comments: ' "$id"; call GET /api/tasks/$id | jq -c '[.comments[]?.body | .[0:500]]'; done

echo "== C. .grain/workflows job declaring a bare name (acme/widgets, ci_mode grain)"
t=$(mktemp -d); git clone -q "$WORK/repos/acme/widgets.git" "$t/w"; mkdir -p "$t/w/.grain/workflows"
cat >"$t/w/.grain/workflows/walk.yml" <<'EOF'
name: walk
on:
  push:
  workflow_dispatch:
jobs:
  bare:
    capabilities: [self-debug]
    steps:
      - run: echo bare
  bareci:
    capabilities: [grain-ci]
    steps:
      - run: echo bareci
EOF
git -C "$t/w" add . && git -C "$t/w" -c user.name=w -c user.email=w@example.com commit -qm "walk workflow" && git -C "$t/w" push -q origin HEAD:walk-ci
SHA=$(git -C "$t/w" rev-parse HEAD); rm -rf "$t"
RUN=$(call POST /api/ci/runs "{\"repo\":\"acme/widgets\",\"ref\":\"walk-ci\",\"commit\":\"$SHA\"}"); echo "$RUN" | jq -c '{id, state, status, error}' 2>/dev/null || echo "$RUN"
RID=$(echo "$RUN" | jq -r '.id // .run.id // empty')
for _ in $(seq 120); do [[ -n "$RID" ]] || break; c=$(call GET "/api/ci/runs/$RID" | jq -r '.conclusion // .run.conclusion // empty'); [[ -n "$c" ]] && break; sleep 1; done
[[ -n "$RID" ]] && call GET "/api/ci/runs/$RID" | jq -c '{conclusion: (.conclusion // .run.conclusion), error: ((.error // .run.error // "") | .[0:900])}'
[[ -n "$RID" ]] && q "SELECT * FROM ci_job WHERE run_id = '$RID'" | cut -c1-900
grep -i 'is refused: it was retired' "$WORK/daemon.log" | cut -c1-600 | head -4 || true
} 2>&1
stop

echo "== D. local policy"
cat >"$WORK/grain_policy.yml" <<'EOF'
repos: [acme/widgets, acme/lib]
local_requesters: [owner@example.com]
capabilities:
  deny: [self-debug, "gcp-key*"]
EOF
cat "$WORK/grain_policy.yml"
DAEMON_EXTRA="-policy $WORK/grain_policy.yml" start
{
grep 'enforcing local policy' "$WORK/daemon.log" | tail -1 | cut -c1-300
printf 'lib + api#self-debug -> '; P1=$(call POST /api/tasks "$(jq -nc --argjson c "$SD" '{title:"policy self-debug",description:"walk",repo:"acme/lib",capabilities:$c,approve:true}')"); echo "$P1" | jq -c '{id,state,error}'
printf 'lib plain (gcp grant written by SQL) -> '; P2=$(call POST /api/tasks '{"title":"policy gcp","description":"walk","repo":"acme/lib","approve":false}'); echo "$P2" | jq -c '{id,state,error}'
P1_ID=$(echo "$P1" | jq -r .id); P2_ID=$(echo "$P2" | jq -r .id)
q "INSERT INTO task_grant (task_id, capability, via, folder) VALUES ('$P2_ID', '$GCP_GRANT', 'label', '')"
for id in $P1_ID $P2_ID; do call POST /api/tasks/$id/approve '{}' | jq -c '{id,state,error}'; done
sleep 12
for id in $P1_ID $P2_ID; do
  printf '%s: ' "$id"; call GET /api/tasks/$id | jq -c '{state, grants: [.grants[]? | (.capability // .)], blocked: (.blockedReason // .policyHold // .heldBy // null), last: ([.comments[]?.body] | last | .[0:400])}'
  [[ -f "$WORK/walk/runs/hook-$id.json" ]] && echo "   ran: hook output exists (NOT expected)" || echo "   no run started"
done
grep -E "candidate ($P1_ID|$P2_ID) skipped" "$WORK/daemon.log" | sed 's/^.*dispatch:/dispatch:/' | sort -u | cut -c1-500 | head -4 || true
} 2>&1
stop; kill "$(cat "$WORK/ghsim.pid")" 2>/dev/null || true
