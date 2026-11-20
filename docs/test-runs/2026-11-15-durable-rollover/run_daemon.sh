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

# docs/test-plans/2026-11-15.md §1 item 1 on a real `grain daemon
# -sandbox-port-base=0` (STORE=sqlite or STORE=postgres), reusing the
# 2026-11-13 health walkthrough's harness (env.sh, ghsim, fake agy).
#
#   1. File a durable task A with a brief, and task W that depends on A.
#   2. A's first run pushes a branch (grain opens a PR in the GitHub sim),
#      writes a handover and comments.
#   3. Back-date A's created_at past model.RolloverAgeLimit (a year) and
#      let orchestrator.reconcileRollover roll it over to B by itself.
#   4. Check B carries brief, handover, PR link and W's wait; wake B and
#      check its dispatched prompt carries the handover.
#   5. Back-date B too, so the chain is A -> B -> C.
# Results go to $WORK_DIR/captures; ids to $WORK_DIR/ids.env.

set -euo pipefail
ME=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-13-grain-health-exit-codes/env.sh
source "$ME/../2026-11-13-grain-health-exit-codes/env.sh"
DAEMON_PORT=${DAEMON_PORT:-4280}
WORK=${WORK:-/tmp/walk-rollover-${STORE:-sqlite}}
MAX_WORKERS=2

init_env "$WORK"
start_ghsim "$WORK"
# start_daemon passes -agy-path "$HERE/fake-agy.py": ours records the prompt.
HERE=$ME
chmod +x "$ME/fake-agy.py"
start_daemon "$WORK"
cap() { echo "$2" >"$WORK_DIR/captures/$1"; }
wait_for() { # wait_for <seconds> <command...>
	local n=$(($1 * 5)); shift
	for _ in $(seq "$n"); do "$@" && return 0; sleep 0.2; done
	return 1
}
state() { api GET "/api/tasks/$1" | jq -r '.state'; }

brief='BRIEF-MARKER: watch the acme/widgets nightly build and file a repair for each red night.'
A=$(api POST /api/tasks "$(jq -nc --arg b "$brief" '{title:"Watch the acme/widgets nightly build (real rollover)",
  description:$b, repo:"acme/widgets", admission:"durable", approved:true}')" | jq -r .id)
W=$(api POST /api/tasks "$(jq -nc --arg a "$A" '{title:"Waits on the nightly watcher", description:"waits",
  repo:"acme/widgets", dependsOn:[$a], approved:true}')" | jq -r .id)
echo "A=$A W=$W" | tee "$WORK_DIR/ids.env"

cat >"$WORK_DIR/action-$A.json" <<'EOF'
[{"kind":"mcp_sequence","calls":[
  {"tool":"run_command","args":{"command":"cd work && date > nightly.txt && git add nightly.txt && git commit -qm 'chore: nightly note\n\nWalkthrough commit.' && git push -q origin HEAD","description":"pushing a branch"}},
  {"tool":"write_handover","args":{"handover":"HANDOVER-MARKER: night 1 was green; next, watch for the flaky shard 3."}},
  {"tool":"comment_on_issue","args":{"comment":"Night 1 watched; handover written."}}]}]
EOF
wait_for 120 bash -c "[[ \$(curl -fsS -H 'Authorization: Bearer $CODE' $DAEMON/api/tasks/$A | jq '.attempts | length') -ge 1 ]] && [[ \$(curl -fsS -H 'Authorization: Bearer $CODE' $DAEMON/api/tasks/$A | jq -r .state) =~ ^(awaiting_|completed|resting) ]]" \
	|| { echo "A's first run did not finish"; api GET "/api/tasks/$A" | jq '{state,attempts:(.attempts|length)}'; exit 1; }
sleep 3
api GET "/api/tasks/$A" >"$WORK_DIR/captures/A-before.json"
jq '{state, admission, attempts: (.attempts|length), pullRequest: (.pullRequest|{number,url}?)}' "$WORK_DIR/captures/A-before.json"

# Every later run (B's, once A rolls over) writes a handover of its own.
cat >"$WORK_DIR/action-default.json" <<'EOF2'
{"kind":"mcp_sequence","calls":[
  {"tool":"write_handover","args":{"handover":"HANDOVER-B-MARKER: night 2 flaked on shard 3; quarantine it next."}},
  {"tool":"comment_on_issue","args":{"comment":"Night 2 watched; handover written."}}]}
EOF2

# Past the age limit: a year and a day.
if [[ ${STORE:-sqlite} == sqlite ]]; then
	store_exec "UPDATE task SET created_at = datetime('now','-400 days') WHERE id = '$A';"
else
	store_exec "UPDATE task SET created_at = now() - interval '400 days' WHERE id = '$A';"
fi
wait_for 60 bash -c "curl -fsS -H 'Authorization: Bearer $CODE' $DAEMON/api/tasks/$A | jq -e '.rolledOverTo' >/dev/null" \
	|| { echo "reconcileRollover did not roll $A over"; grep -i rollover "$WORK_DIR/daemon.log" | tail; exit 1; }
B=$(api GET "/api/tasks/$A" | jq -r .rolledOverTo)
echo "B=$B" | tee -a "$WORK_DIR/ids.env"
grep "rolled durable task $A over" "$WORK_DIR/daemon.log" | tee "$WORK_DIR/captures/rollover-log.txt"
api GET "/api/tasks/$A" >"$WORK_DIR/captures/A-after.json"
api GET "/api/tasks/$B" >"$WORK_DIR/captures/B.json"
api GET "/api/tasks/$W" >"$WORK_DIR/captures/W.json"

# Let B's first run finish (it writes HANDOVER-B-MARKER) before rolling it.
wait_for 120 bash -c "[[ \$(curl -fsS -H 'Authorization: Bearer $CODE' $DAEMON/api/tasks/$B | jq '.attempts | length') -ge 1 ]] && [[ \$(curl -fsS -H 'Authorization: Bearer $CODE' $DAEMON/api/tasks/$B | jq -r .state) =~ ^(awaiting_|completed|resting) ]]" \
	|| { echo "B's first run did not finish"; exit 1; }
sleep 2

# The chain: B past the limit too, with the id sequence moved on so the
# third task's id has five digits (plan item 4).
if [[ ${STORE:-sqlite} == sqlite ]]; then
	store_exec "INSERT INTO task_sequence (number, issued_at) VALUES (12344, datetime('now'));"
	store_exec "UPDATE task SET created_at = datetime('now','-400 days') WHERE id = '$B';"
else
	store_exec "SELECT setval(pg_get_serial_sequence('task_sequence','number'), 12344);"
	store_exec "UPDATE task SET created_at = now() - interval '400 days' WHERE id = '$B';"
fi
wait_for 60 bash -c "curl -fsS -H 'Authorization: Bearer $CODE' $DAEMON/api/tasks/$B | jq -e '.rolledOverTo' >/dev/null" \
	|| { echo "reconcileRollover did not roll $B over"; exit 1; }
C=$(api GET "/api/tasks/$B" | jq -r .rolledOverTo)
echo "C=$C" | tee -a "$WORK_DIR/ids.env"
long='Watch the acme/widgets nightly build and keep its flaky integration-suite quarantine list, release-notes digest and dependency-bump backlog current across every supported branch'
api PATCH "/api/tasks/$C" "$(jq -nc --arg t "$long" '{title:$t}')" >/dev/null
for t in "$A" "$B" "$C" "$W"; do api GET "/api/tasks/$t" >"$WORK_DIR/captures/final-$t.json"; done
jq -c '{id, state, rolledOverFrom, rolledOverTo, pullRequest, dependsOn}' "$WORK_DIR"/captures/final-*.json
api GET /api/inbox >"$WORK_DIR/captures/inbox.json" 2>/dev/null || true
body=$(jq -r .description "$WORK_DIR/captures/final-$C.json")
printf '%s\n' "$body" >"$WORK_DIR/captures/C-description.md"
for m in BRIEF-MARKER '## Handover' HANDOVER-B-MARKER HANDOVER-MARKER: 'Rolled over from'; do
	echo "C's description: $m x$(grep -o -- "$m" <<<"$body" | wc -l)"
done
echo "ok: $STORE A=$A B=$B C=$C W=$W"
