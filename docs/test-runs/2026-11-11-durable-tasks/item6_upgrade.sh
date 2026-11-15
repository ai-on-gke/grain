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

# §1 item 6: a store at schema 131 (61668e58) holding a long-running
# durable task, upgraded in place by this checkout's grain.
# Usage: item6_upgrade.sh <old-grain> <new-grain> [daemon flags, e.g. -postgres-url URL]
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
OLD=$1 NEW=$2; shift 2
GRAIN=$OLD # so init_env seeds the store with the old build
source "$HERE/env.sh"
W=${W:-/tmp/walk-upgrade}
init_env "$W"
start_ghsim "$W"
echo '{"kind":"resume_later","mcp_args":{"reason":"walkthrough park","when":{"at":"1h"}}}' >"$W/action-default.json"
start_daemon "$OLD" "$W" "$@"
echo "old daemon schema: $(grep -o 'schema[^,]*13[0-9][^,]*' "$W/daemon.log" | head -1)"
T=$(api POST /api/tasks '{"title":"Long-running durable task","description":"UPGRADE-ORIGINAL: keep acme/widgets green.","repo":"acme/widgets","admission":"durable","approve":true}' | jq -r .id)
api POST "/api/tasks/$T/approve" '{}' >/dev/null || true
for i in $(seq 25); do api POST "/api/tasks/$T/comments" "{\"body\":\"pre-upgrade note $i\"}" >/dev/null; done
sleep 8
echo "before: $(api GET "/api/tasks/$T" | jq -c '{id,admission,state,comments:(.comments|length),attempts:(.attempts|length)}')"
stop_env "$W"; rm -f "$W/daemon.pid"
if [[ -n "${PREPARE:-}" ]]; then
	# The Upgrade button's path: export the old store, rehearse the import,
	# and let the next start migrate from the export (a dump/import round trip).
	echo "prepare-upgrade: $("$NEW" state -data-dir "$W/data" prepare-upgrade "$@" 2>&1 | tail -1)"
fi

start_daemon "$NEW" "$W" "$@"
echo "after:  $(api GET "/api/tasks/$T" | jq -c '{id,admission,state,comments:(.comments|length),attempts:(.attempts|length)}')"
echo "context: $(api GET "/api/tasks/$T/context" | jq -c '{current_brief,handover_version,compacted_through,original_description,age:.handover_age,stale:.handover_stale}')"
GRAIN_SERVER=$DAEMON GRAIN_TOKEN=$CODE "$NEW" brief "$T" | head -3
echo "page: $(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $CODE" "$DAEMON/tasks/$T")"
echo "== grain state sync + check"
"$NEW" state -data-dir "$W/data" sync 2>&1 | tail -1
"$NEW" state check "$W/data/state-repo" 2>&1 | tail -1
echo "TASK=$T"
