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

# §5 item 4: Add grant… on /next/ and on the classic task pane adds
# api:grain/operator and repo#credential {credential: bot}; each is stored
# canonically (GET /api/tasks/{id}) and the next run holds it (its
# prompt carries the operator preset's api sections and repo#credential's
# capability block). Also re-clicks Approve on item 2's seeded rows when
# they exist. Needs `make frontend` built into the grain binary and
# ui/node_modules (npm ci in ui/).
#
#   STORE=sqlite|postgres ./item4_grants.sh [WORK]

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

WORK=${1:-/tmp/walk-admission-${STORE:-sqlite}}
WORK_DIR=$WORK
start_ghsim "$WORK" || true
MAX_WORKERS=2 start_daemon "$GRAIN" "$WORK"
echo '{"kind":"nothing_needed","reason":"walkthrough default completion"}' >"$WORK/action-default.json"
export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}

if [[ $(api GET /api/tasks/6 | jq -r '.state') == proposed ]]; then
	echo "== item 2 from the UIs (seeded rows 3, 4, 6)"
	node "$HERE/item4_ui.mjs" approve "$DAEMON" "$CODE" "$WORK/shots" 3 4 6
fi

mk() { api POST /api/tasks "{\"title\":\"$1\",\"description\":\"grant target\",\"repo\":\"acme/widgets\"}" | jq -r .id; }
N=$(mk "Item4 next grants")
C=$(mk "Item4 classic grants")
echo "== item 4: /next/ task $N, classic task $C"
node "$HERE/item4_ui.mjs" grants "$DAEMON" "$CODE" "$WORK/shots" "$N" "$C"

for id in "$N" "$C"; do api POST "/api/tasks/$id/approve" >/dev/null; done
for _ in $(seq 120); do
	[[ -f "$WORK/runs/prompt-$N-1.txt" && -f "$WORK/runs/prompt-$C-1.txt" ]] && break
	sleep 0.5
done
echo "== what each run was handed (operator preset sections, repo#credential block)"
for id in "$N" "$C"; do
	python3 - "$WORK/runs/prompt-$id-1.txt" "$id" <<'EOF'
import json, sys
s = json.loads(open(sys.argv[1]).read())["message"]["content"]
i = s.find('<capability grant="https://grain.dev/capabilities/repo#credential"')
bot = 'the "bot" GitHub token' in s
print("task %s: api#test-quarantine section=%s, api#self-debug section=%s, repo#credential block=%s, says bot token=%s"
      % (sys.argv[2], "api#test-quarantine" in s, "api#self-debug" in s, i >= 0, bot))
EOF
done
