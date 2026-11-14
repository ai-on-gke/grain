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

# §1 item 3: item3_next_ui.mjs against a live daemon. STORE=sqlite|postgres.
# Needs `npm ci` in ui/ (Playwright) and PLAYWRIGHT_BROWSERS_PATH.

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
STORE=${STORE:-sqlite}
WORK_DIR="/tmp/walk-st-item3-${STORE}"
trap 'stop_env "$WORK_DIR"' EXIT

init_env "$WORK_DIR"
start_ghsim "$WORK_DIR"
start_daemon "$GRAIN" "$WORK_DIR"
api PUT /api/repos/acme/widgets/declarations '{"declarations":true}' >/dev/null
DECL="repo:template:acme:widgets:plain-note"
for _ in $(seq 50); do api GET /api/templates | jq -e --arg id "$DECL" '.[] | select(.id == $id)' >/dev/null && break; sleep 0.2; done
DEPLOY=$(api POST /api/templates '{"name":"deploy-walk","title":"Deployment walk task","description":"Filed from a deployment template."}' | jq -r .id)
PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright} \
	node "$HERE/item3_next_ui.mjs" "$DAEMON" "$CODE" "${SHOTS:-$WORK_DIR/shots}" "$DECL" "$DEPLOY"
echo "PASS: Item 3 ($STORE)"
