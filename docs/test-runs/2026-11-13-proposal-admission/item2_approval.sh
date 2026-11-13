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

# §5 item 2: proposals stored before PRs #1257/#1435 (seeded straight
# into the store) that hold a CI-only grant or an over-maximum sandbox
# shape are refused at approval. Run after item1_relay.sh on the same
# WORK (it reuses proposals 3-6 and the sandboxShapeLimits it set).
# The /next/ and classic buttons are driven by item2_ui.mjs.
#
#   STORE=sqlite|postgres ./item2_approval.sh [WORK]

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

WORK=${1:-/tmp/walk-admission-${STORE:-sqlite}}
WORK_DIR=$WORK
[[ -f "$WORK/daemon.pid" ]] && kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null ||
	{ start_ghsim "$WORK" || true; start_daemon "$GRAIN" "$WORK"; }
CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
export CODE

sql() {
	if [[ ${STORE:-sqlite} == postgres ]]; then
		psql -qAt "${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk?sslmode=disable}" -c "$1"
	else
		sqlite3 "$WORK/data/store/grain.db" "$1"
	fi
}

echo "== seed pre-change proposal rows"
sql "INSERT INTO task_grant(task_id, capability, via) VALUES ('6', 'repo-read:acme/gadgets', 'label')"
sql "INSERT INTO task_grant(task_id, capability, via) VALUES ('3', 'https://grain.dev/capabilities/repo#write={\"repo\":\"acme/widgets\"}', 'label')"
sql "UPDATE task SET sandbox_disk_gb = 500 WHERE id = '4'"
sql "UPDATE task SET sandbox_cpus = 16, sandbox_memory_mb = 65536 WHERE id = '5'"

for id in 3 4 5 6; do
	echo "-- POST /api/tasks/$id/approve"
	curl -sS -o "$WORK/approve-$id.json" -w '%{http_code}\n' -X POST -H "Authorization: Bearer $CODE" "$DAEMON/api/tasks/$id/approve"
	cat "$WORK/approve-$id.json"; echo
	echo "   state now: $(api GET "/api/tasks/$id" | jq -r '.state')"
done

echo "== run-now on an unapproved seeded proposal (the other approval route)"
curl -sS -w ' %{http_code}\n' -X POST -H "Authorization: Bearer $CODE" "$DAEMON/api/tasks/6/run-now"
