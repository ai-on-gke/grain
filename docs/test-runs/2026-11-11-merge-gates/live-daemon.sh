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

# live-daemon.sh: walks §4 items 1-4 of docs/test-plans/2026-11-11.md against a
# real `grain daemon -sandbox-port-base=0 -host-sandboxes` over SQLite or
# PostgreSQL, with githubsim (`ghsim.go`), `.grain/triggers/*` fixtures in a
# simulated repository, real grain CI, and `fake-agy.sh` + `hook_merge_gates.py`
# stubbing only the model provider.
set -euo pipefail

DB_DRIVER="${1:-sqlite}"
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
HEAD_BIN="${HEAD_BIN:-/home/debian/bin/grain}"
WORK="/var/tmp/mergegates/$DB_DRIVER"
OUT="${OUT:-$WORK/out}"
GHSIM_ADDR="127.0.0.1:4284"
PORT=4285
IAP_ADDR="127.0.0.1:4283"

cleanup() {
  for p in "$WORK/daemon.pid" "$WORK/ghsim.pid" "$WORK/iap.pid"; do
    [[ -f "$p" ]] && kill "$(cat "$p")" 2>/dev/null || true
  done
}
trap cleanup EXIT

rm -rf "$WORK"
mkdir -p "$WORK/repos/acme" "$WORK/data" "$WORK/scratch" "$WORK/home" "$WORK/walk/runs" "$OUT" /var/tmp/mergegates
printf '%s' "$WORK/walk" >/var/tmp/mergegates/current-walk-dir
cp "$HERE/hook_merge_gates.py" "$WORK/walk/hook.py"
chmod +x "$HERE/fake-agy.sh"

go build -o "$WORK/ghsim" "$HERE/ghsim.go"
go build -o "$WORK/iap_keys" "$HERE/iap_keys.go"
"$WORK/iap_keys" -addr "$IAP_ADDR" -aud walk-aud -iss https://cloud.google.com/iap >"$WORK/iap.log" 2>&1 &
echo $! >"$WORK/iap.pid"
git init -q --bare -b main "$WORK/repos/acme/widgets.git"
seed_tmp=$(mktemp -d)
git -C "$seed_tmp" init -q -b main
echo "# acme/widgets" >"$seed_tmp/README.md"
cp -r "$HERE/seed/widgets/." "$seed_tmp/"
mv "$seed_tmp/dot-grain" "$seed_tmp/.grain"
git -C "$seed_tmp" add .
git -C "$seed_tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "seed widgets with triggers and CI"
git -C "$seed_tmp" push -q "$WORK/repos/acme/widgets.git" main
rm -rf "$seed_tmp"

"$WORK/ghsim" -addr "$GHSIM_ADDR" -root "$WORK/repos" -repo acme/widgets=main >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"

DB_ARGS=()
if [[ "$DB_DRIVER" == "postgres" ]]; then
  sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grainwalk;" -c "CREATE DATABASE grainwalk OWNER debian;"
  DB_ARGS=(-db-driver postgres -db-dsn "postgres://debian:walk@127.0.0.1:5432/grainwalk?sslmode=disable")
fi

export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0 WALK_DIR="$WORK/walk"
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN

"$HEAD_BIN" daemon -data-dir "$WORK/data" -ui-addr "127.0.0.1:$PORT" -sandbox-port-base=0 \
  -host-sandboxes -sandbox-dir "$WORK/scratch" -sandbox-disk-floor-share 0 -sandbox-disk-floor-min-mb 0 \
  -poll-interval 1s -as owner@example.com -default-target-repo acme/widgets \
  -ui-iap-keys-url "http://$IAP_ADDR/keys" -ui-iap-audience walk-aud -ui-iap-issuer https://cloud.google.com/iap \
  -github-host "$GHSIM_ADDR" -github-insecure-http "${DB_ARGS[@]}" >"$WORK/daemon.log" 2>&1 &
echo $! >"$WORK/daemon.pid"

for _ in $(seq 300); do
  kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null || { tail -n 40 "$WORK/daemon.log"; exit 1; }
  if [[ -s "$WORK/data/local-api-token" ]] && \
    curl -fs -o /dev/null -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")" "http://127.0.0.1:$PORT/api/config"; then
    break
  fi
  sleep 0.2
done
CODE=$(cat "$WORK/data/local-api-token")

call() {
  local a=(-sS -X "$1" -H "Authorization: Bearer $CODE")
  [[ -n "${3:-}" ]] && a+=(-H "Content-Type: application/json" --data "$3")
  curl "${a[@]}" "http://127.0.0.1:$PORT$2"
}
gcli() {
  GRAIN_SANDBOX_CLI=0 GRAIN_SERVER="http://127.0.0.1:$PORT" GRAIN_TOKEN="$CODE" "$HEAD_BIN" "$@"
}

call PUT /api/github-tokens/default '{"value":"ghp_walkthrough_stub"}' >/dev/null
call POST /api/repos '{"repo":"acme/widgets"}' >/dev/null
call PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"grain"}' >/dev/null
call PUT /api/repos/acme/widgets/declarations '{"declarations":true}' >/dev/null
call PUT /api/config "{\"externalAgentApi\":true,\"maxConcurrentSandboxes\":6,\"agentBackends\":[{\"id\":\"ag-walk\",\"name\":\"Walk Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$HERE/fake-agy.sh\",\"models\":[\"fake-model\"]}]}" >/dev/null
call PUT /api/agent-backends/ag-walk/credential '{"value":"walk-fake-gemini-key"}' >/dev/null

for _ in $(seq 60); do
  n=$(call GET /api/templates | jq '(if type=="array" then . else .templates end) | map(select(.id | startswith("repo:template:acme:widgets:"))) | length')
  [[ "$n" -ge 2 ]] && break
  sleep 0.5
done
for did in $(call GET /api/declarations | jq -r '.[]?.id'); do
  seen=$(call GET /api/declarations | jq -c --arg id "$did" '.[] | select(.id==$id) | .requested')
  call POST /api/declarations/accept "{\"id\":\"$did\",\"seen\":$seen}" >/dev/null || true
done

echo "=== [$DB_DRIVER] Item 1 & 2 (refusals) & 3 (findings-only push refused) ==="
touch "$WORK/walk/hold-reviews"
T1=$(call POST /api/tasks '{"title":"Touch boundary and triggers","description":"IMPL_MARKER_FULL_GATES","repo":"acme/widgets","autoMerge":true,"approved":true}' | jq -r .id)

PR1=""
for _ in $(seq 120); do
  PR1=$(call GET "/api/tasks/$T1" | jq -r '.pullRequest // empty')
  trig_n=$(call GET "/api/tasks/$T1" | jq '(.pathTriggerTasks // []) | length')
  sec_id=$(call GET "/api/tasks/$T1" | jq -r '.securityReviewTask // empty')
  [[ -n "$PR1" && "$trig_n" -ge 3 && -n "$sec_id" ]] && break
  sleep 0.5
done
SEC1=$(call GET "/api/tasks/$T1" | jq -r '.securityReviewTask')
TRIG_IDS=$(call GET "/api/tasks/$T1" | jq -r '[(.pathTriggerTasks // [])[] | split("=")[1]] | unique | join(",")')
echo "T1=$T1 PR1=$PR1 securityReview=$SEC1 distinctTriggerReviews=$TRIG_IDS (from $(call GET "/api/tasks/$T1" | jq -r '(.pathTriggerTasks // []) | join(" ")'))"

echo "-- Submit before reviews finish --"
gcli pr submit "$PR1" --json | jq -c '{pr,state,approved,overridden,gates:[.gates[]|"\(.id)=\(.state) (\(.detail))"]}'
gcli merge-queue | head -20

echo "-- Re-judging the same head files nothing --"
sleep 3
TRIG_AFTER=$(call GET "/api/tasks/$T1" | jq -r '[(.pathTriggerTasks // [])[] | split("=")[1]] | unique | join(",")')
[[ "$TRIG_IDS" == "$TRIG_AFTER" ]] || { echo "FAIL: duplicate review filed ($TRIG_IDS -> $TRIG_AFTER)"; exit 1; }
echo "re-judged same head: distinctTriggerReviews still $TRIG_AFTER"

echo "-- Override refusals --"
set +e
R_MISSING=$(gcli pr submit "$PR1" --override-gates=security-review --reason="outage" 2>&1); RC_MISSING=$?
R_EMPTY=$(gcli pr submit "$PR1" --override-gates=security-review,review --reason="   " 2>&1); RC_EMPTY=$?
set -e
echo "missing gate (rc=$RC_MISSING): $R_MISSING"
echo "empty reason (rc=$RC_EMPTY): $R_EMPTY"
[[ "$RC_MISSING" -ne 0 && "$RC_EMPTY" -ne 0 ]]

call POST /api/access/principals '{"entry":"user:owner@example.com","role":"grain/operator","reason":"walkthrough"}' >/dev/null
call POST /api/access/principals '{"entry":"user:bob@example.com","role":"grain/task-admin","reason":"walkthrough"}' >/dev/null
BOB_JWT=$(curl -sS "http://$IAP_ADDR/mint?email=bob@example.com")
BOB_SUBMIT=$(curl -sS -o "$WORK/bob-sub.json" -w "%{http_code}" -H "X-Goog-IAP-JWT-Assertion: $BOB_JWT" -H "Content-Type: application/json" \
  -d "{\"pr\":\"$PR1\",\"overrideGates\":[\"security-review\",\"review\"],\"reason\":\"outage\"}" "http://127.0.0.1:$PORT/api/pull-requests/submit")
BOB_FM=$(curl -sS -o "$WORK/bob-fm.json" -w "%{http_code}" -H "X-Goog-IAP-JWT-Assertion: $BOB_JWT" -H "Content-Type: application/json" \
  -d '{"overrideGates":["security-review","review"],"reason":"outage"}' "http://127.0.0.1:$PORT/api/tasks/$T1/force-merge")
echo "limited role bob: pr/submit=$BOB_SUBMIT ($(cat "$WORK/bob-sub.json")) force-merge=$BOB_FM ($(cat "$WORK/bob-fm.json"))"
[[ "$BOB_SUBMIT" == "403" && "$BOB_FM" == "403" ]]

AG_TOK=$(call POST /api/external-agents/tokens '{"name":"walk-agent","scopes":["tasks:read","tasks:write"]}' | jq -r .token)
AG_SUBMIT=$(curl -sS -o "$WORK/ag-sub.json" -w "%{http_code}" -H "Authorization: Bearer $AG_TOK" -H "Content-Type: application/json" \
  -d "{\"pr\":\"$PR1\",\"overrideGates\":[\"security-review\",\"review\"],\"reason\":\"outage\"}" "http://127.0.0.1:$PORT/api/pull-requests/submit")
AG_FM=$(curl -sS -o "$WORK/ag-fm.json" -w "%{http_code}" -H "Authorization: Bearer $AG_TOK" -H "Content-Type: application/json" \
  -d '{"overrideGates":["security-review","review"],"reason":"outage"}' "http://127.0.0.1:$PORT/api/tasks/$T1/force-merge")
echo "agent token: pr/submit=$AG_SUBMIT ($(cat "$WORK/ag-sub.json")) force-merge=$AG_FM ($(cat "$WORK/ag-fm.json"))"
[[ "$AG_SUBMIT" == "403" && "$AG_FM" == "403" ]]

echo "-- Release PR1 reviews (security clean, planner clean, sensitive tries push + leaves finding) --"
for rid in "$SEC1" ${TRIG_IDS//,/ }; do
  touch "$WORK/walk/allow-$rid"
done
for rid in "$SEC1" ${TRIG_IDS//,/ }; do
  for _ in $(seq 120); do
    st=$(call GET "/api/tasks/$rid" | jq -r .state)
    [[ "$st" == "completed" || "$st" == "closed" || "$st" == "awaiting_submit" || "$st" == "awaiting_reply" ]] && break
    sleep 0.5
  done
done
sleep 4
for rid in ${TRIG_IDS//,/ }; do
  role=$(jq -r '.role // empty' "$WORK/walk/runs/hook-$rid.json" 2>/dev/null || true)
  if [[ "$role" == "trigger_sensitive" ]]; then
    echo "sensitive review ($rid) push_attempt:"
    jq -c '{prompt_has_push_instruction, push_attempt, pr_open_attempt}' "$WORK/walk/runs/hook-$rid.json"
    jq -e '.prompt_has_push_instruction == false and (.push_attempt.text | contains("read-only") or contains("403")) and (.push_attempt.text | contains("exit=128"))' "$WORK/walk/runs/hook-$rid.json" >/dev/null
  fi
done
PR_COUNT=$(curl -sS "http://$GHSIM_ADDR/repos/acme/widgets/pulls?state=all" | jq 'length')
echo "total PRs on ghsim after review runs = $PR_COUNT (want 1)"
[[ "$PR_COUNT" == "1" ]]

T1_AFTER_FINDINGS=$(call GET "/api/tasks/$T1" | jq -c '{state, gates:[.mergeGates[]|"\(.id)=\(.state) (\(.detail))"]}')
echo "T1 after findings (still open after Submit!): $T1_AFTER_FINDINGS"
PR1_NUM=${PR1#*#}
PR1_MERGED=$(curl -sS "http://$GHSIM_ADDR/repos/acme/widgets/pulls/$PR1_NUM" | jq -r .merged)
[[ "$PR1_MERGED" == "false" ]]

echo "-- Push new head on T1 -> debounced incremental re-review --"
T1_BRANCH=$(call GET "/api/tasks/$T1" | jq -r .branch)
fix_tmp=$(mktemp -d)
git clone -q -b "$T1_BRANCH" "$WORK/repos/acme/widgets.git" "$fix_tmp"
echo "token: rotated-via-vault" >"$fix_tmp/config/secrets/prod.yml"
git -C "$fix_tmp" -c user.name=walk -c user.email=walk@example.com commit -qam "fix: rotate token"
git -C "$fix_tmp" push -q origin "$T1_BRANCH"
rm -rf "$fix_tmp"

REREV=""
for _ in $(seq 60); do
  NEW_TRIG=$(call GET "/api/tasks/$T1" | jq -r '[(.pathTriggerTasks // [])[] | split("=")[1]] | unique | join(",")')
  [[ "$NEW_TRIG" != "$TRIG_IDS" ]] && { REREV="$NEW_TRIG"; break; }
  sleep 0.5
done
echo "new head trigger reviews: $REREV"
FIRST_NEW=$(echo "$REREV" | tr ',' '\n' | tail -n 1)
call GET "/api/tasks/$FIRST_NEW" | jq -c '{id, state, deferredUntil, incremental: ((.description // .body // "") | contains("This is an incremental review."))}'
call GET "/api/tasks/$FIRST_NEW" | jq -e '.deferredUntil != null and ((.description // .body // "") | contains("This is an incremental review."))' >/dev/null
sleep 3
AGAIN_TRIG=$(call GET "/api/tasks/$T1" | jq -r '[(.pathTriggerTasks // [])[] | split("=")[1]] | unique | join(",")')
[[ "$AGAIN_TRIG" == "$REREV" ]] || { echo "FAIL: re-judging new head refiled"; exit 1; }

echo "=== [$DB_DRIVER] Item 2 (valid partial override) & Item 4 (post-merge review follow-up) ==="
T2=$(call POST /api/tasks '{"title":"Partial override and post-merge review","description":"IMPL_MARKER_POST_MERGE","repo":"acme/widgets","autoMerge":true,"approved":true}' | jq -r .id)
PR2=""
for _ in $(seq 120); do
  PR2=$(call GET "/api/tasks/$T2" | jq -r '.pullRequest // empty')
  trig2=$(call GET "/api/tasks/$T2" | jq '(.pathTriggerTasks // []) | length')
  sec2=$(call GET "/api/tasks/$T2" | jq -r '.securityReviewTask // empty')
  [[ -n "$PR2" && "$trig2" -ge 1 && -n "$sec2" ]] && break
  sleep 0.5
done
SEC2=$(call GET "/api/tasks/$T2" | jq -r '.securityReviewTask')
TRIG2=$(call GET "/api/tasks/$T2" | jq -r '[(.pathTriggerTasks // [])[] | split("=")[1]] | unique | .[0]')
PR2_NUM=${PR2#*#}
touch "$WORK/walk/allow-$SEC2"

for _ in $(seq 120); do
  sec_state=$(call GET "/api/tasks/$T2" | jq -r '.mergeGates[] | select(.id=="security-review") | .state')
  ci_state=$(call GET "/api/tasks/$T2" | jq -r '.mergeGates[] | select(.id=="ci") | .state')
  [[ "$sec_state" == "passed" && "$ci_state" == "passed" && -f "$WORK/walk/runs/started-$TRIG2" ]] && break
  sleep 0.5
done
echo "T2 before partial override: $(call GET "/api/tasks/$T2" | jq -c '[.mergeGates[]|"\(.id)=\(.state)"]')"

gcli pr submit "$PR2" --override-gates=review --reason="emergency mitigation" --json | jq -c '{pr,overridden,gates:[.gates[]|"\(.id)=\(.state) (\(.detail))"]}'

for _ in $(seq 60); do
  m=$(curl -sS "http://$GHSIM_ADDR/repos/acme/widgets/pulls/$PR2_NUM" | jq -r .merged)
  [[ "$m" == "true" ]] && break
  sleep 0.5
done
echo "PR2 merged=$(curl -sS "http://$GHSIM_ADDR/repos/acme/widgets/pulls/$PR2_NUM" | jq -r .merged)"
[[ "$(curl -sS "http://$GHSIM_ADDR/repos/acme/widgets/pulls/$PR2_NUM" | jq -r .merged)" == "true" ]]
echo "T2 gateOverride audit: $(call GET "/api/tasks/$T2" | jq -c '.gateOverride')"

touch "$WORK/walk/allow-$TRIG2"
FOLLOWUP=""
for _ in $(seq 80); do
  FOLLOWUP=$(call GET /api/tasks | jq -r '(if type=="array" then . else .tasks end) | map(select(.title | startswith("Fix post-merge review findings")))[0].id // empty')
  [[ -n "$FOLLOWUP" ]] && break
  sleep 0.5
done
echo "post-merge follow-up task: $(call GET "/api/tasks/$FOLLOWUP" | jq -c '{id,title,state,base,hasFinding:((.description // .body // "") | contains("post-merge leaked token"))}')"
call GET "/api/tasks/$FOLLOWUP" | jq -e '((.description // .body // "") | contains("post-merge leaked token"))' >/dev/null

echo "=== [$DB_DRIVER] Item 3b & 1b (Submit before review; security review commits on change's branch; PR merges once review & CI pass) ==="
T3=$(call POST /api/tasks '{"title":"Security review fixes in place","description":"IMPL_MARKER_SEC_INPLACE","repo":"acme/widgets","autoMerge":true,"approved":true}' | jq -r .id)
SEC3=""; PR3=""
for _ in $(seq 120); do
  SEC3=$(call GET "/api/tasks/$T3" | jq -r '.securityReviewTask // empty')
  PR3=$(call GET "/api/tasks/$T3" | jq -r '.pullRequest // empty')
  [[ -n "$SEC3" && -n "$PR3" ]] && break
  sleep 0.5
done
PR3_NUM=${PR3#*#}
gcli pr submit "$PR3" --json | jq -c '{pr,approved,gates:[.gates[]|"\(.id)=\(.state)"]}'
printf '%s' "$SEC3" >"$WORK/walk/inplace-commit-review-id"
touch "$WORK/walk/allow-$SEC3"
for _ in $(seq 120); do
  [[ -s "$WORK/walk/runs/hook-$SEC3.json" ]] && break
  sleep 0.5
done
T3_BRANCH=$(call GET "/api/tasks/$T3" | jq -r .branch)
echo "security review ($SEC3) branch & in-place push on $T3_BRANCH:"
jq -c '{branch, inplace_push}' "$WORK/walk/runs/hook-$SEC3.json"
jq -e --arg b "$T3_BRANCH" '(.branch.text | contains($b)) and (.inplace_push.text | contains("exit=0"))' "$WORK/walk/runs/hook-$SEC3.json" >/dev/null
for _ in $(seq 80); do
  m3=$(curl -sS "http://$GHSIM_ADDR/repos/acme/widgets/pulls/$PR3_NUM" | jq -r .merged)
  [[ "$m3" == "true" ]] && break
  sleep 0.5
done
echo "PR3 merged automatically after security review + CI passed: $(curl -sS "http://$GHSIM_ADDR/repos/acme/widgets/pulls/$PR3_NUM" | jq -r .merged)"
[[ "$(curl -sS "http://$GHSIM_ADDR/repos/acme/widgets/pulls/$PR3_NUM" | jq -r .merged)" == "true" ]]

echo "=== [$DB_DRIVER] ALL LIVE DAEMON CHECKS PASSED ==="
