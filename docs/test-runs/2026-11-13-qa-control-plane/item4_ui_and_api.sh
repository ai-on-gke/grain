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

# Item 4 (§2): Drive `/next/settings/tasks` over a real `grain daemon` (SQLite
# and PostgreSQL) to verify UI/CLI agreement, inline validation, viewer
# read-only banner + 403 forced POST, and two-tab non-stomp edits.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

work="/tmp/walk-qa-item4-${STORE:-sqlite}"
trap 'stop_env "$work"' EXIT

init_env "$work" "$GRAIN"
start_ghsim "$work"
start_daemon "$GRAIN" "$work"
sleep 1.0

echo "=== [Item 4 / store=${STORE:-sqlite}] ==="
NODE_PATH="$ROOT/ui-ng/node_modules" \
PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}" \
DAEMON="$DAEMON" \
CODE="$CODE" \
GRAIN="$GRAIN" \
SHOT_DIR="${SHOT_DIR:-}" \
node "$HERE/item4_ui.mjs"
