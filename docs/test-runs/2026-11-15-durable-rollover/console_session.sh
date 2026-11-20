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

# docs/test-plans/2026-11-15.md §1 item 5, over the daemon run_daemon.sh
# left running (same STORE and WORK): start a console session, back-date
# its task past the age limit, post a message to it, and check the prompt
# its next turn gets carries the record-limit note -- and that
# reconcileRollover leaves the session alone.

set -euo pipefail
ME=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-13-grain-health-exit-codes/env.sh
source "$ME/../2026-11-13-grain-health-exit-codes/env.sh"
WORK=${WORK:-/tmp/walk-rollover-${STORE:-sqlite}}
export WORK_DIR=$WORK
CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")

api POST /api/console-sessions '{"id":"walk-rollover","title":"Walk rollover session"}' >"$WORK/captures/session.json"
S=$(api GET /api/tasks | jq -r '(if type=="array" then . else .tasks end)[] | select(.title=="Walk rollover session") | .id' | head -n1)
[[ -n "$S" ]] || { echo "no task for the session"; cat "$WORK/captures/session.json"; exit 1; }
echo "S=$S" | tee -a "$WORK/ids.env"
if [[ ${STORE:-sqlite} == sqlite ]]; then
	store_exec "UPDATE task SET created_at = datetime('now','-400 days') WHERE id = '$S';"
else
	store_exec "UPDATE task SET created_at = now() - interval '400 days' WHERE id = '$S';"
fi
before=$(ls "$WORK/runs" | grep -c '^prompt-' || true)
cat >"$WORK/action-$S.json" <<'EOF'
{"kind":"comment_on_issue","comment":"Noted: this session is due to be closed and refiled."}
EOF
api POST "/api/tasks/$S/comments" '{"body":"How did last night go?"}' >/dev/null
for _ in $(seq 300); do
	n=$(ls "$WORK/runs" | grep -c '^prompt-' || true)
	if ((n > before)); then
		last=$(ls "$WORK/runs" | grep '^prompt-' | sort | tail -n1)
		grep -q "You are running as task $S" "$WORK/runs/$last" && break
	fi
	sleep 0.2
done
last=$(ls "$WORK/runs" | grep '^prompt-' | sort | tail -n1)
echo "prompt: $last ($(grep -o "You are running as task [0-9]*" "$WORK/runs/$last" | head -n1))"
echo "record-limit note: x$(grep -o "This console session is past a durable task's record limit" "$WORK/runs/$last" | wc -l)"
grep -o "past a durable task's record limit: it has [^.]*" "$WORK/runs/$last" | head -n1
sleep 3
api GET "/api/tasks/$S" | jq -c '{id, state, rolledOverTo}' | tee "$WORK/captures/session-after.json"
